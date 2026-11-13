#!/bin/sh
# Runs the gaon operator against a real garam and a real control
# service, with a stand-in for gaon's API, and prints what happened. The
# stack is garam's authz engine, api and machine listener, run as host
# processes from the `garam` binary, the test-principal fixture and the
# migrations that `make garam-e2e` copies out of garam's test image at
# GARAM_REVISION (GARAM_DIR names them), over a PostgreSQL server
# compose starts; the control service is built from this checkout
# (cmd/control). The operator and the stand-in (tests/gaon/gaonstandin,
# which serves tests/gaon/gaonfake over mutual TLS) are built from this
# checkout and run on the host. Each step prints its request and the
# answer's status and body, a token shortened to its header, claims and
# the first 12 characters of its signature. A step whose answer differs
# from the one the script names stops the run non-zero.
#
# What the run seeds rather than does through a service, and why:
# - the owner's user and session, by garam's test-principal fixture,
#   which makes the one signed-in user the public routes cannot: a
#   person signs in through a browser, which a script cannot;
# - the control service's profile, through its own publish-profile
#   subcommand, which control.md gives as the way a deployment
#   publishes one;
# - the gaon API's state: the stand-in states a MicroVM's status through
#   its admin path, as a Node Agent would, and the model-key Secret is
#   put through the stand-in's ordinary Secret route, as a person with
#   gaon access would.
# Nothing else is written outside a service's own route.
#
# PAIR_RUN_GAON names the mode, which is what serves gaon's API:
# - standin (the default): tests/gaonstandin, as above;
# - gaon (`make pair-run-gaon`): gaon's own built services from the
#   commit GAON_COMMIT of the checkout GAON_REPO, with authorization on
#   (tests/pairrun/gaon-build.sh, tests/pairrun/gaonharness): an
#   embedded etcd, gaon-apiserver with --authorization-mode=RBAC,
#   gaon-controllers, gaon-scheduler and one gaon-node-agent over a
#   minimal mvmd that runs nothing. The harness issues the operator a
#   client certificate with CN garam-gaon-operator and no Organization,
#   and the run, as the administrator, binds it the role the document
#   states. The model-key Secret is put as the administrator and the
#   MicroVM's status is written by the Node Agent, so the stand-in's
#   admin path is not used.
#
# PAIR_RUN_PROJECT names the compose project. Nothing is pushed, tagged
# or deleted in a registry.
set -eu

cd "$(dirname "$0")/../.."
module=$(pwd)
here=$module/tests/pairrun
garam_dir=${GARAM_DIR:?names the directory make garam-e2e filled with garam, testprincipal and _migrations}
for f in garam testprincipal _migrations; do
	[ -e "$garam_dir/$f" ] || {
		echo "pair-run: $garam_dir/$f is missing: run it through make pair-run, which runs make garam-e2e first" >&2
		exit 1
	}
done

mode=${PAIR_RUN_GAON:-standin}
gaon_repo=${GAON_REPO:-}
gaon_commit=${GAON_COMMIT:-2387cebd0b44b322b7da2ff485144a7456a97c6f}
case $mode in
standin) ;;
gaon)
	[ -n "$gaon_repo" ] || {
		echo "pair-run: GAON_REPO names the gaon checkout to clone for the gaon mode" >&2
		exit 1
	}
	;;
*)
	echo "pair-run: PAIR_RUN_GAON is standin or gaon, not '$mode'" >&2
	exit 1
	;;
esac
project=${PAIR_RUN_PROJECT:-pairrun}
checked=0
started=
operator_pid=
standin_pid=
harness_pid=
gaon_scratch=
garam_pids=
control_pid=

tmp=$(mktemp -d)
mkdir "$tmp/pub" "$tmp/bin"
chmod 755 "$tmp/pub"

compose() {
	docker compose -p "$project" -f "$here/docker-compose.yml" "$@"
}

# A free loopback port.
freeport() {
	python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

cleanup() {
	status=$?
	trap - EXIT
	if [ "$status" -ne 0 ]; then
		if [ -n "$harness_pid" ]; then
			# The harness copies each service's log out when it stops.
			kill "$harness_pid" 2>/dev/null || true
			wait "$harness_pid" 2>/dev/null || true
			harness_pid=
			for log in "$tmp"/gaonout/logs/*.log; do
				[ ! -s "$log" ] || {
					echo "pair-run: last lines of $(basename "$log"):" >&2
					tail -n 15 "$log" >&2
				}
			done
		fi
		if [ -s "$tmp/microvm.json" ]; then
			echo "pair-run: the MicroVM's status when last read:" >&2
			jq -c '{spec: {nodeName: .spec.nodeName, bindingEpoch: .spec.bindingEpoch}, status}' "$tmp/microvm.json" | cut -c1-600 >&2
		fi
		for log in operator standin harness control; do
			[ ! -s "$tmp/$log.log" ] || {
				echo "pair-run: last lines of the $log log:" >&2
				tail -n 25 "$tmp/$log.log" >&2
			}
		done
		for log in authz api machine control; do
			[ ! -s "$tmp/$log.log" ] || {
				echo "pair-run: last lines of the $log log:" >&2
				tail -n 15 "$tmp/$log.log" >&2
			}
		done
	fi
	[ -z "$operator_pid" ] || kill "$operator_pid" 2>/dev/null || true
	[ -z "$standin_pid" ] || kill "$standin_pid" 2>/dev/null || true
	[ -z "$control_pid" ] || kill "$control_pid" 2>/dev/null || true
	for pid in $garam_pids; do kill "$pid" 2>/dev/null || true; done
	if [ -n "$harness_pid" ]; then
		kill "$harness_pid" 2>/dev/null || true
		wait "$harness_pid" 2>/dev/null || true
	fi
	[ -z "$gaon_scratch" ] || rm -rf "$gaon_scratch"
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$tmp"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

fail() {
	echo "pair-run: FAILED: $1" >&2
	exit 1
}

say() { printf '%s\n' "$*"; }

step() { printf '\n== step %s: %s\n' "$1" "$2"; }

# An assertion: what it checks, what came back, what was named.
expect() {
	if [ "$2" = "$3" ]; then
		checked=$((checked + 1))
		say "  ok: $1: $2"
	else
		fail "$1: got '$2', want '$3'"
	fi
}

b64u() { basenc --base64url -w0 | tr -d =; }

b64pad() {
	pad=$(((4 - ${#1} % 4) % 4))
	printf '%s%s' "$1" "$(printf '====' | head -c "$pad")"
}
b64d() { b64pad "$1" | tr '_-' '/+' | base64 -d 2>/dev/null; }

# Every compact token in the text, shortened to what a reader needs.
shorten() {
	text=$(cat)
	for jwt in $(printf '%s' "$text" | grep -oE 'eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*' | sort -u); do
		rest=${jwt#*.}
		short="<jwt header $(b64d "${jwt%%.*}") claims $(b64d "${rest%%.*}") signature $(printf '%s' "${rest#*.}" | cut -c1-12)...>"
		text=$(printf '%s' "$text" | sed "s|$jwt|$short|g")
	done
	printf '%s\n' "$text"
}

# A credential that is not a compact token: its first 12 characters.
mask() {
	case $1 in
	eyJ*.*.*) printf '%s' "$1" | shorten ;;
	*) printf '%s...' "$(printf '%s' "$1" | cut -c1-12)" ;;
	esac
}

# JSON with every credential that is not a signed token masked, and the
# data of a Secret replaced by what it is.
redact() {
	jq -c 'walk(if type == "object" and ((.data? | type) == "object") then .data |= map_values("<redacted>") else . end)
		| walk(if type == "object" then
		with_entries(if (.key | IN("password", "privateKeyPem", "token", "authority", "operationRef", "access_token", "client_assertion"))
			and (.value | type == "string") and (.value | startswith("eyJ") | not)
			then .value = "<opaque>" else . end)
		else . end)' 2>/dev/null || cat
}

# call METHOD URL [json BODY | form FIELDS]: one request, printed with
# its answer. The status is left in $status and the body in $tmp/body.
# $bearer, $authorization, $cookie, $extra_header and $curl_extra, where
# set, ride on it.
bearer=
authorization=
cookie=
extra_header=
curl_extra=
call() {
	method=$1
	url=$2
	kind=${3:-}
	data=${4:-}
	say "> $method $url"
	set -- -sS -X "$method" -o "$tmp/body" -w '%{http_code}' -H 'Garam-Contract-Version: operation-authority.v1'
	if [ -n "$bearer" ]; then
		set -- "$@" -H "Authorization: Bearer $bearer"
		say "> authorization: Bearer $(mask "$bearer")"
	fi
	if [ -n "$authorization" ]; then
		set -- "$@" -H "Authorization: Garam-Operation $authorization"
		say "> authorization: Garam-Operation $(mask "$authorization")"
	fi
	if [ -n "$extra_header" ]; then
		set -- "$@" -H "$extra_header"
		say "> $extra_header"
	fi
	if [ -n "$cookie" ]; then
		set -- "$@" -H "Cookie: $cookie_name=$cookie"
		say "> cookie: $cookie_name=<opaque>"
	fi
	case $kind in
	json)
		set -- "$@" -H 'Content-Type: application/json' --data "$data"
		printf '> %s\n' "$(printf '%s' "$data" | redact | shorten)"
		;;
	form)
		for field in $data; do
			set -- "$@" --data-urlencode "$field"
			case ${field%%=*} in
			client_assertion) value=$(mask "${field#*=}") ;;
			*) value=${field#*=} ;;
			esac
			printf '> %s=%s\n' "${field%%=*}" "$value"
		done
		;;
	esac
	# shellcheck disable=SC2086
	status=$(curl $curl_extra "$@" "$url") || fail "curl could not reach $url"
	say "< $status"
	if [ -s "$tmp/body" ]; then
		if jq -e . "$tmp/body" >/dev/null 2>&1; then
			redact <"$tmp/body" | shorten | sed 's/^/< /'
		else
			shorten <"$tmp/body" | sed 's/^/< /'
		fi
	fi
}

jget() { jq -r "$1" "$tmp/body"; }

jti() { openssl rand -base64 24 | tr '+/' '-_' | tr -d =; }

# The RFC 7638 thumbprint of an RSA key, which is the kid garam reads.
modulus() { openssl rsa -in "$1" -noout -modulus | cut -d= -f2 | basenc --base16 -d | b64u; }
thumbprint() {
	printf '{"e":"AQAB","kty":"RSA","n":"%s"}' "$(modulus "$1")" | openssl dgst -sha256 -binary | b64u
}

# The assertion an operator presents: KEY KID CLIENT-ID AUDIENCE.
assertion() {
	now=$(date +%s)
	h=$(printf '{"alg":"RS256","kid":"%s","typ":"JWT"}' "$2" | b64u)
	c=$(printf '{"iss":"%s","sub":"%s","aud":"%s","iat":%s,"exp":%s,"jti":"%s"}' "$3" "$3" "$4" "$now" "$((now + 30))" "$(jti)" | b64u)
	s=$(printf '%s.%s' "$h" "$c" | openssl dgst -sha256 -sign "$1" -binary | b64u)
	printf '%s.%s.%s' "$h" "$c" "$s"
}

assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer

sha256hex() { printf '%s' "$1" | openssl dgst -sha256 -hex | awk '{print $NF}'; }

# wait_for WHAT SECONDS COMMAND...: tries COMMAND once a second.
wait_for() {
	what=$1
	limit=$2
	shift 2
	i=0
	until "$@" >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -lt "$limit" ] || fail "$what: not within ${limit}s"
		sleep 1
	done
}

# ---------------------------------------------------------------------
# Step 1: the keys, the stack, the owner's session.

compose down -v --remove-orphans >/dev/null 2>&1 || true
commit=$(git rev-parse HEAD)
dirty=
git diff --quiet HEAD -- . 2>/dev/null || dirty=" (working tree differs from it)"

step 1 "the stack: garam's api, authorization engine and machine listener, the control service, a database"
say "commit: $commit$dirty"
say "garam: $(cat "$garam_dir/provenance"), run as host processes; the control service is built from this checkout"

# The machine listener's certificate names localhost and 127.0.0.1, and
# is its own root. The control service's names the same.
selfsigned() {
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=$1" \
		-addext "subjectAltName=$2" -addext "basicConstraints=critical,CA:TRUE" \
		-keyout "$tmp/$3.key" -out "$tmp/$3.crt" 2>/dev/null
}
selfsigned machine "DNS:localhost,IP:127.0.0.1" machine
selfsigned control "DNS:localhost,IP:127.0.0.1" control-api
openssl rand -hex 32 >"$tmp/kek.hex"

# In the stand-in mode, a CA of the run signs the stand-in's
# certificate, which names localhost, and the operator's client
# certificate. In the gaon mode the harness makes gaon's CA and every
# certificate under it.
if [ "$mode" = standin ]; then
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=pair-run gaon CA" \
		-addext "basicConstraints=critical,CA:TRUE" -keyout "$tmp/gaon-ca.key" -out "$tmp/gaon-ca.crt" 2>/dev/null
	issue_gaon() {
		openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$1" \
			-keyout "$tmp/$2.key" -out "$tmp/$2.csr" 2>/dev/null
		printf '%s\n' "$3" >"$tmp/$2.ext"
		openssl x509 -req -in "$tmp/$2.csr" -CA "$tmp/gaon-ca.crt" -CAkey "$tmp/gaon-ca.key" -CAcreateserial \
			-days 2 -extfile "$tmp/$2.ext" -out "$tmp/$2.crt" 2>/dev/null
	}
	issue_gaon localhost gaon-server "subjectAltName=DNS:localhost,IP:127.0.0.1"
	issue_gaon gaon-operator operator-client "extendedKeyUsage=clientAuth"
fi
chmod 600 "$tmp"/*.key "$tmp/kek.hex" 2>/dev/null || true
say "keys: made for this run in a directory only this user reads, removed with the run"

started=1
compose up -d --wait postgres >"$tmp/up.log" 2>&1 || {
	cat "$tmp/up.log" >&2
	fail "postgres did not come up"
}
pg_port=$(compose port postgres 5432 | sed 's/.*://')
server_url="postgres://garam:garam@127.0.0.1:$pg_port/postgres?sslmode=disable"
compose exec -T postgres createdb -U garam control || fail "the control service's database was not made"
"$garam_dir/testprincipal" prepare -contract 1 -migrations "$garam_dir/_migrations" -server-url "$server_url" \
	>"$tmp/principal.json" || fail "testprincipal did not prepare garam's database"
[ "$(jq -r .contract "$tmp/principal.json")" = 1 ] || fail "testprincipal answered under no contract 1"
database_url=$(jq -r .databaseUrl "$tmp/principal.json")

grpc=127.0.0.1:$(freeport)
authz_metrics=127.0.0.1:$(freeport)
api_addr=127.0.0.1:$(freeport)
machine_addr=127.0.0.1:$(freeport)
machine_health=127.0.0.1:$(freeport)
export GARAM_DATABASE_URL=$database_url GARAM_AUTHZ_ADDR=$grpc GARAM_MACHINE_TOKEN_URL=https://$machine_addr/oauth/token
GARAM_GRPC_ADDR=$grpc GARAM_METRICS_ADDR=$authz_metrics "$garam_dir/garam" serve authz >"$tmp/authz.log" 2>&1 &
garam_pids="$garam_pids $!"
GARAM_HTTP_ADDR=$api_addr GARAM_COOKIE_SECURE=false "$garam_dir/garam" serve api >"$tmp/api.log" 2>&1 &
garam_pids="$garam_pids $!"
GARAM_MACHINE_ADDR=$machine_addr GARAM_MACHINE_HEALTH_ADDR=$machine_health GARAM_MACHINE_TLS_CERT_FILE=$tmp/machine.crt \
	GARAM_MACHINE_TLS_KEY_FILE=$tmp/machine.key GARAM_MACHINE_SERVER_ROOT_FILE=$tmp/machine.crt \
	GARAM_CA_KEY_FILE=$tmp/kek.hex GARAM_MACHINE_ISSUER=https://$machine_addr \
	"$garam_dir/garam" serve machine >"$tmp/machine.log" 2>&1 &
garam_pids="$garam_pids $!"
unset GARAM_DATABASE_URL GARAM_AUTHZ_ADDR GARAM_MACHINE_TOKEN_URL
authz_ready() { curl -fsS "http://$authz_metrics/readyz"; }
api_ready() { curl -fsS "http://$api_addr/readyz"; }
machine_ready() { curl -fsS "http://$machine_health/readyz"; }
wait_for "garam's authorization engine" 60 authz_ready
wait_for "garam's api" 60 api_ready
wait_for "garam's machine listener" 60 machine_ready
api=http://$api_addr
machine=https://$machine_addr
machine_curl="--cacert $tmp/machine.crt"
expect "api is ready" "$(curl -sS -o /dev/null -w '%{http_code}' "$api/readyz")" 200
expect "machine is ready" "$(curl -sS -o /dev/null -w '%{http_code}' "http://$machine_health/readyz")" 200

# ---------------------------------------------------------------------
step 2 "an organization, its manager and control service registered as an owner would"
say "sign-in here: garam's test-principal fixture made the owner and its session;"
say "everything after goes through garam's routes"
owner_token=$(jq -r .session.token "$tmp/principal.json")
cookie_name=$(jq -r .session.cookie "$tmp/principal.json")
owner_grn=$(jq -r .userGrn "$tmp/principal.json")
suffix=$(openssl rand -hex 4)
say "owner: $owner_grn and its session"

cookie=$owner_token
call POST "$api/orgs" json "{\"name\":\"Pair run $suffix\"}"
expect "organization created" "$status" 201
org_grn=$(jget .grn)
org=${org_grn##*/}
call POST "$api/orgs/$org/operators" json '{"identifier":"gaon","name":"gaon operator","controlService":{"identifier":"gaon-control","name":"gaon control service"}}'
expect "manager and control service registered" "$status" 201
manager_grn=$(jget .grn)
control_grn=grn:$org/iam/operator/gaon-control
expect "manager's GRN" "$manager_grn" "grn:$org/iam/operator/gaon"

# The rotation is `immediate` on purpose: it withdraws every live key
# and works for an operator with none. `planned` adds a key beside one
# in use and has not been run here; do not change it without a run.
issue_key() {
	call POST "$api/orgs/$org/operators/$1/keys" json '{"rotation":"immediate"}'
	expect "key issued to $1" "$status" 201
	jget .privateKeyPem >"$tmp/$2.key"
	chmod 600 "$tmp/$2.key"
	jget .clientId >"$tmp/$2-client-id"
	jget .tokenUrl >"$tmp/$2-token-url"
	expect "$1's client id" "$(cat "$tmp/$2-client-id")" "grn:$org/iam/operator/$1"
}
issue_key gaon operator
issue_key gaon-control control-operator
token_url=$(cat "$tmp/operator-token-url")
say "token URL: $token_url"
# The control service reads these as files, and the copy in its volume
# is made at its start without a trailing newline to remove.
printf '%s' "$(cat "$tmp/control-operator-client-id")" >"$tmp/control-client-id"
printf '%s' "$(cat "$tmp/control-operator-token-url")" >"$tmp/control-token-url"
mv "$tmp/control-operator.key" "$tmp/control-operator.key.issued"
cp "$tmp/control-operator.key.issued" "$tmp/control-operator.key"
chmod 600 "$tmp/control-operator.key"

expires=$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ)
delegation=$(jq -nc --arg id "pair-run-delegation-$suffix" --arg manager "$manager_grn" --arg exp "$expires" \
	'{requestId: $id, controllers: [$manager], operations: ["iam:agent.create", "iam:agent.configure", "iam:agent.activate",
	"iam:agent.recover", "iam:agent-template.read", "iam:agent-template.publish", "iam:execution-profile.read",
	"iam:agent.execution-read", "iam:operator.read"], expiresAt: $exp}')
call PUT "$api/orgs/$org/operators/gaon-control/delegation" json "$delegation"
expect "delegation written" "$status" 201
cookie=

# ---------------------------------------------------------------------
step 3 "the control service, its profile, and the console's view of an owner's authority"
( cd "$module" && go build -o "$tmp/bin/control" ./cmd/control ) || fail "the control service did not build"
control_db_url="postgres://garam:garam@127.0.0.1:$pg_port/control?sslmode=disable"
control_port=$(freeport)
control_health_port=$(freeport)
CONTROL_DATABASE_URL=$control_db_url "$tmp/bin/control" --api-certificate-file="$tmp/control-api.crt" \
	--api-key-file="$tmp/control-api.key" --api-bind-address=127.0.0.1:$control_port \
	--health-probe-bind-address=127.0.0.1:$control_health_port --garam-machine-url="$machine" \
	--garam-server-root-file="$tmp/machine.crt" --operator-private-key-file="$tmp/control-operator.key" \
	--operator-client-id-file="$tmp/control-client-id" --operator-token-url-file="$tmp/control-token-url" \
	>"$tmp/control.log" 2>&1 &
control_pid=$!
control=https://localhost:$control_port
control_health=http://127.0.0.1:$control_health_port
control_curl="--cacert $tmp/control-api.crt"
control_ready() { curl -fsS "$control_health/readyz"; }
wait_for "the control service's readiness" 60 control_ready
say "the control service is ready: it migrated its schema and reached garam"

cat >"$tmp/pub/profile.yaml" <<EOF
organization: $org
name: standard
version: 1
resources:
  requests: {cpu: 500m, memory: 512Mi}
  limits: {cpu: "1", memory: 1Gi}
storageSize: 1Gi
workspaceStorageSize: 1Gi
EOF
chmod 644 "$tmp/pub/profile.yaml"
say "> control publish-profile --file profile.yaml (the service's subcommand, run once)"
CONTROL_DATABASE_URL=$control_db_url "$tmp/bin/control" publish-profile --file "$tmp/pub/profile.yaml" >"$tmp/profile.log" 2>&1 || {
	cat "$tmp/profile.log" >&2
	fail "publish-profile was refused"
}
sed 's/^/< /' "$tmp/profile.log"
checked=$((checked + 1))
say "  ok: the profile standard v1 is published"

# mint OPERATION TARGET REQUEST-ID BODY [REQUEST-TARGET]: the owner has
# garam mint an authority over exactly BODY for the control service.
mint() {
	req=$(jq -nc --arg a "$control_grn" --arg o "$1" --arg t "$2" --arg r "$3" --arg d "$(sha256hex "$4")" --arg rt "${5:-}" \
		'{audience: $a, operation: $o, target: $t, requestId: $r, bodySha256: $d} + (if $rt == "" then {} else {requestTarget: $rt} end)')
	cookie=$owner_token
	call POST "$api/orgs/$org/operation-authorities" json "$req"
	cookie=
	expect "authority for $1" "$status" 201
	authority=$(jget .authority)
}

# ---------------------------------------------------------------------
gaon_ca_file=$tmp/gaon-ca.crt
operator_cert=$tmp/operator-client.crt
operator_key=$tmp/operator-client.key
if [ "$mode" = standin ]; then
	step 4 "gaon's stand-in, and a model key for the organization"
	( cd "$module" && go build -o "$tmp/bin/gaonstandin" ./tests/gaon/gaonstandin && go build -o "$tmp/bin/gaon-operator" ./cmd/gaon-operator ) ||
		fail "the stand-in and the operator did not build"
	"$tmp/bin/gaonstandin" -addr 127.0.0.1:0 -addr-file "$tmp/gaon.addr" -cert-file "$tmp/gaon-server.crt" \
		-key-file "$tmp/gaon-server.key" -client-ca-file "$tmp/gaon-ca.crt" >"$tmp/standin.log" 2>&1 &
	standin_pid=$!
	addr_ready() { [ -s "$tmp/gaon.addr" ]; }
	wait_for "the stand-in's address" 20 addr_ready
	gaon=https://localhost:$(sed 's/.*://' "$tmp/gaon.addr")
	gaon_curl="--cacert $gaon_ca_file --cert $operator_cert --key $operator_key"
	admin_curl=$gaon_curl
	say "stand-in: tests/gaonfake over mutual TLS at $gaon"
else
	step 4 "gaon's own services with authorization on, and a model key for the organization"
	( cd "$module" && go build -o "$tmp/bin/gaon-operator" ./cmd/gaon-operator ) || fail "the operator did not build"
	git clone --quiet --no-hardlinks "$gaon_repo" "$tmp/gaon" || fail "gaon did not clone from $gaon_repo"
	sh "$here/gaon-build.sh" "$tmp/gaon" "$gaon_commit" "$tmp/gaonbin" >"$tmp/gaon-build.log" 2>&1 || {
		cat "$tmp/gaon-build.log" >&2
		fail "gaon did not build at $gaon_commit"
	}
	say "gaon: $(git -C "$tmp/gaon" rev-parse HEAD), built from a clone of $gaon_repo"
	gaon_scratch=$(mktemp -d /tmp/gaon-1862-XXXXXX)
	mkdir "$tmp/gaonout"
	"$tmp/gaonbin/gaonharness1862" -dir "$gaon_scratch" -bin "$tmp/gaonbin" -out "$tmp/gaonout" >"$tmp/harness.log" 2>&1 &
	harness_pid=$!
	addr_ready() { [ -s "$tmp/gaonout/address" ]; }
	wait_for "gaon's services and the Node to be Ready" 180 addr_ready
	gaon_root=https://localhost:$(sed 's/.*://' "$tmp/gaonout/address")
	gaon=$gaon_root/apis/compute.gaon.garam.sh/v1alpha1
	rbac=$gaon_root/apis/rbac.gaon.garam.sh/v1alpha1
	gaon_ca_file=$tmp/gaonout/ca.crt
	operator_cert=$tmp/gaonout/operator.crt
	operator_key=$tmp/gaonout/operator.key
	gaon_curl="--cacert $gaon_ca_file --cert $operator_cert --key $operator_key"
	admin_curl="--cacert $gaon_ca_file --cert $tmp/gaonout/admin.crt --key $tmp/gaonout/admin.key"
	say "gaon-apiserver (--authorization-mode=RBAC, mutual TLS), gaon-controllers, gaon-scheduler and one"
	say "gaon-node-agent are running at $gaon_root; the Node is Admitted and Ready"
	say "the operator's certificate: $(openssl x509 -in "$operator_cert" -noout -subject)"

	# The role is the one the operator's document and ADR-0082 and docs/architecture/gaon-operator.md
	# state: microvms, volumes and secrets limited by label selector to
	# what the operator labels, watch of microvms, and the model-key
	# Secrets read by list. An administrator creates it: gaon's escalation
	# rule lets only a holder of every permission bind a role.
	role=$(jq -nc '{apiVersion: "rbac.gaon.garam.sh/v1alpha1", kind: "ClusterRole", metadata: {name: "garam-gaon-operator"},
		rules: [
		{apiGroups: ["compute.gaon.garam.sh"], resources: ["microvms", "volumes", "secrets"],
		 verbs: ["list", "create", "update", "delete"], labelSelector: "garam.sh/role=agent"},
		{apiGroups: ["compute.gaon.garam.sh"], resources: ["microvms"], verbs: ["watch"], labelSelector: "garam.sh/role=agent"},
		{apiGroups: ["compute.gaon.garam.sh"], resources: ["secrets"], verbs: ["list"], labelSelector: "garam.sh/role=model-key"}]}')
	binding=$(jq -nc '{apiVersion: "rbac.gaon.garam.sh/v1alpha1", kind: "ClusterRoleBinding", metadata: {name: "garam-gaon-operator"},
		roleRef: {kind: "ClusterRole", name: "garam-gaon-operator"}, subjects: [{kind: "User", name: "garam-gaon-operator"}]}')
	agent_selector='labelSelector=garam.sh%2Frole%3Dagent'

	curl_extra=$gaon_curl
	call GET "$gaon/microvms?$agent_selector"
	curl_extra=
	expect "before its binding, gaon refuses the operator's list of its MicroVMs" "$status" 403
	curl_extra=$admin_curl
	call PUT "$rbac/clusterroles/garam-gaon-operator" json "$role"
	expect "the role is created by the administrator" "$status" 200
	call PUT "$rbac/clusterrolebindings/garam-gaon-operator" json "$binding"
	expect "the binding is created by the administrator" "$status" 200
	curl_extra=$gaon_curl
	call GET "$gaon/microvms?$agent_selector"
	curl_extra=
	expect "after its binding, the same list is answered" "$status" 200
	expect "and holds no MicroVM yet" "$(jget '.items | length')" 0
fi

org_label=$(sha256hex "$org_grn" | cut -c1-32)
model_key=sk-pair-run-$(openssl rand -hex 12)
secret=$(jq -nc --arg org "$org_label" --arg key "$(printf '%s' "$model_key" | base64 -w0)" \
	'{apiVersion: "compute.gaon.garam.sh/v1alpha1", kind: "Secret",
	metadata: {name: "model-keys", labels: {"garam.sh/role": "model-key", "garam.sh/organization": $org}},
	data: {web: $key}}')
curl_extra=$admin_curl
call PUT "$gaon/secrets/model-keys" json "$secret"
expect "model-key Secret stored" "$status" 200
curl_extra=

# ---------------------------------------------------------------------
step 5 "an agent is created through the control service's console route"
template_body=$(jq -nc --arg id "pair-run-publish-$suffix" \
	'{requestId: $id, profile: {name: "standard", version: 1}, configuration: {
	model: {provider: "anthropic", baseUrl: "https://api.anthropic.com", name: "claude-opus-5-5", apiKeyRef: "model-keys/web",
	embedding: {baseUrl: "https://embeddings.example/v1", name: "bge-base-en-v1.5"}},
	ego: "Answers the questions of the pair run.", tools: {}}}')
publish_path=/v1/orgs/$org/templates/pair-run/versions
mint iam:agent-template.publish "$org_grn" "pair-run-publish-$suffix" "$template_body" "$publish_path"
authorization=$authority
curl_extra=$control_curl
call POST "$control$publish_path" json "$template_body"
authorization=
curl_extra=
expect "template published" "$status" 201

create_body=$(jq -nc --arg id "pair-run-create-$suffix" --arg manager "$manager_grn" \
	'{requestId: $id, controller: $manager, template: {name: "pair-run", version: 1}, profile: {name: "standard", version: 1}}')
mint iam:agent.create "$manager_grn" "pair-run-create-$suffix" "$create_body"
authorization=$authority
curl_extra=$control_curl
call POST "$control/v1/orgs/$org/agents" json "$create_body"
authorization=
curl_extra=
expect "agent created" "$status" 201
agent_grn=$(jget .agent)
epoch=$(jget .epoch)
expect "revision" "$(jget .revision)" 1
agent_id=${agent_grn##*/}
name=agent-$(sha256hex "$agent_grn" | cut -c1-16)
say "agent: $agent_grn at epoch $epoch; its objects in gaon are named $name"

cookie=$owner_token
call GET "$api/orgs/$org/agents/$agent_id"
cookie=
expect "garam reads the agent" "$status" 200
expect "garam's view before any report" "$(jget .provisioningState)" pending

# ---------------------------------------------------------------------
step 6 "the manager reads the feed as the operator does: the feed holds the agent"
now_assertion=$(assertion "$tmp/operator.key" "$(thumbprint "$tmp/operator.key")" "$manager_grn" "$token_url")
curl_extra=$machine_curl
call POST "$machine/oauth/token" form "grant_type=client_credentials client_id=$manager_grn client_assertion_type=$assertion_type client_assertion=$now_assertion audience=$control_grn"
curl_extra=
expect "token exchange for the control service" "$status" 200
manager_token=$(jget .access_token)
bearer=$manager_token
extra_header="Controller-Grn: $manager_grn"
curl_extra=$control_curl
call GET "$control/v1/operators/self/desired?waitSeconds=0"
bearer=
extra_header=
curl_extra=
expect "feed status" "$status" 200
expect "the feed holds the agent" "$(jq -r --arg g "$agent_grn" '[.agents[].agent] | index($g) != null' "$tmp/body")" true
expect "the feed's epoch" "$(jq -r --arg g "$agent_grn" '.agents[] | select(.agent == $g) | .epoch' "$tmp/body")" "$epoch"

# ---------------------------------------------------------------------
step 7 "gaon-operator, built from this checkout, runs as the registered manager"
# AGENT_CPU, AGENT_MEMORY and AGENT_IMAGE are required and checked at
# load; the figures and the zero digest are this run's, not the
# operator's.
env GAON_API_URL="$gaon" GAON_CA_FILE="$gaon_ca_file" GAON_CLIENT_CERT_FILE="$operator_cert" \
	GAON_CLIENT_KEY_FILE="$operator_key" GARAM_MACHINE_URL="$machine" GARAM_ROOT_FILE="$tmp/machine.crt" \
	GARAM_TOKEN_URL="$token_url" CONTROL_URL="$control" CONTROL_ROOT_FILE="$tmp/control-api.crt" \
	CONTROL_GRN="$control_grn" OPERATOR_KEY_FILE="$tmp/operator.key" OPERATOR_CLIENT_ID="$manager_grn" \
	AGENT_CPU=1 AGENT_MEMORY=1Gi \
	AGENT_IMAGE=registry.invalid/gaon-guest@sha256:0000000000000000000000000000000000000000000000000000000000000000 HEALTH_ADDR=127.0.0.1:0 \
	"$tmp/bin/gaon-operator" >"$tmp/operator.log" 2>&1 &
operator_pid=$!
say "gaon-operator started as $manager_grn"

registered() { grep -qF "registered the agent's placement" "$tmp/operator.log"; }
wait_for "the placement's registration" 120 registered
say "the operator's log:"
sed 's/^/  /' "$tmp/operator.log"
checked=$((checked + 1))
say "  ok: the control service answered 201 to the placement (the operator logs 'registered the agent's placement' for a 201, and 'already registered' for a 200)"

owner_state() {
	curl -fsS -H "Cookie: $cookie_name=$owner_token" "$api/orgs/$org/agents/$agent_id" | jq -r .provisioningState
}
state_is() { [ "$(owner_state)" = "$1" ]; }
wait_for "garam's view of the agent to read provisioned" 60 state_is provisioned
cookie=$owner_token
call GET "$api/orgs/$org/agents/$agent_id"
cookie=
expect "garam's view after the operator's report" "$(jget .provisioningState)" provisioned
reports=$(grep -c '"operation":"POST /agents/{agent}/provisioning-state","status":204' "$tmp/machine.log" || true)
expect "garam's machine listener answered the operator's report 204 (once or more)" "$([ "$reports" -ge 1 ] && echo yes || echo "none: $reports")" yes

# ---------------------------------------------------------------------
step 8 "what the operator wrote into gaon's API, read back"
# The operator's role has no get, so the gaon mode reads as the
# administrator and then reads the operator's way, by list, below.
read_curl=$gaon_curl
[ "$mode" = standin ] || read_curl=$admin_curl
curl_extra=$read_curl
call GET "$gaon/secrets/$name"
expect "secret read" "$status" 200
expect "secret's role label" "$(jget '.metadata.labels["garam.sh/role"]')" agent
expect "secret's organization label" "$(jget '.metadata.labels["garam.sh/organization"]')" "$org_label"
expect "secret's keys" "$(jget '.data | keys | join(",")')" "agent-token,control-root.pem,control-token,garam-root.pem,placement-token"
credential_values=$(jq -r '.data | to_entries[] | select(.key | IN("agent-token", "control-token", "placement-token")) | .value' "$tmp/body" |
	while read -r v; do printf '%s\n' "$(printf '%s' "$v" | base64 -d)"; done)
expect "three credentials were read from the secret" "$(printf '%s\n' "$credential_values" | grep -c .)" 3

call GET "$gaon/volumes/$name-memory"
expect "memory volume read" "$status" 200
call GET "$gaon/volumes/$name-workspace"
expect "workspace volume read" "$status" 200

call GET "$gaon/microvms/$name"
expect "microvm read" "$status" 200
curl_extra=
expect "microvm's user" "$(jget .spec.user)" 0:0
expect "microvm's run strategy" "$(jget .spec.runStrategy)" Always
expect "microvm's volumes" "$(jget '[.spec.volumes[].name] | sort | join(",")')" "$name-memory,$name-workspace"
expect "microvm's files" "$(jget '[.spec.files[].path] | sort | join(",")')" \
	"/run/garam/agent-token,/run/garam/control-root.pem,/run/garam/control-token,/run/garam/garam-root.pem,/run/garam/placement-token,/run/garam/runtime-config.yaml,/run/garam/runtime-ego.md"
expect "microvm's credential files come from the secret, mode 0400, owned by 65532" \
	"$(jget '[.spec.files[] | select(.valueFrom.secretKeyRef.name == "'"$name"'") | "\(.mode)/\(.owner.uid)"] | unique | join(",")')" 256/65532
expect "microvm's model key is the Secret's, by reference" \
	"$(jget '.spec.env[] | select(.name == "MODEL_API_KEY") | "\(.valueFrom.secretKeyRef.name)/\(.valueFrom.secretKeyRef.key) value=\(.value // "none")"')" "model-keys/web value=none"
leaks=0
for value in $credential_values "$model_key"; do
	if grep -qF -- "$value" "$tmp/body"; then
		leaks=$((leaks + 1))
	fi
done
expect "no credential, and not the model key, is inline in the microvm" "$leaks" 0

if [ "$mode" = standin ]; then
	puts() { grep -c "method=PUT path=$1" "$tmp/standin.log" || true; }
	expect "PUTs of the agent's volumes" "$(puts "/volumes/$name-")" 2
	expect "PUTs of the agent's secret" "$(puts "/secrets/$name")" 1
	expect "PUTs of the agent's microvm" "$(puts "/microvms/$name")" 1
	say "the stand-in's request log:"
	sed 's/^/  /' "$tmp/standin.log"
else
	# The objects as the operator reads them: by list, with the selector
	# its role is limited to.
	curl_extra=$gaon_curl
	call GET "$gaon/microvms?$agent_selector"
	expect "the operator lists its MicroVMs" "$status" 200
	expect "and finds the agent's" "$(jget '[.items[].metadata.name] | join(",")')" "$name"
	call GET "$gaon/volumes?$agent_selector"
	expect "the operator lists its Volumes" "$status" 200
	expect "and finds the agent's two" "$(jget '[.items[].metadata.name] | sort | join(",")')" "$name-memory,$name-workspace"
	call GET "$gaon/secrets?$agent_selector"
	expect "the operator lists its Secrets" "$status" 200
	expect "and finds the agent's" "$(jget '[.items[].metadata.name] | join(",")')" "$name"
	expect "gaon returns the Secret's data on a list" "$(jget '.items[0].data | keys | length')" 5
	call GET "$gaon/secrets?labelSelector=garam.sh%2Frole%3Dmodel-key"
	expect "the operator lists the model-key Secrets" "$status" 200
	expect "and finds the organization's" "$(jget '[.items[].metadata.name] | join(",")')" model-keys
	expect "with its data" "$(jget '.items[0].data | keys | join(",")')" web
	call GET "$gaon/secrets?labelSelector=garam.sh%2Frole%3Dagent,garam.sh%2Forganization%3D$org_label"
	expect "a list by an organization's digest answers" "$status" 200
	expect "and finds the agent's Secret" "$(jget '.items | length')" 1
	call GET "$gaon/microvms?labelSelector=garam.sh%2Frole%3Dagent,garam.sh%2Forganization%3D0000"
	expect "an absent object is read as empty by list" "$(jget '.items | length')" 0
	curl_extra=
fi

# ---------------------------------------------------------------------
if [ "$mode" = standin ]; then
	step 9 "the stand-in reports the MicroVM Running and Ready, as a Node Agent would"
	curl_extra=$gaon_curl
	call PUT "$gaon/_admin/microvms/$name/status" json '{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}'
	curl_extra=
	expect "status stored" "$status" 204
else
	step 9 "gaon's scheduler binds the MicroVM and its Node Agent starts it over the minimal mvmd"
	running() {
		curl -fsS $admin_curl "$gaon/microvms/$name" >"$tmp/microvm.json" || return 1
		jq -e '.status.phase == "Running"' "$tmp/microvm.json"
	}
	wait_for "the MicroVM to be Running" 120 running
	curl_extra=$admin_curl
	call GET "$gaon/microvms/$name"
	curl_extra=
	expect "the scheduler bound it to the Node" "$(jget .spec.nodeName)" node-1862
	expect "the binding epoch" "$(jget .spec.bindingEpoch)" 1
	expect "the Node Agent reports the same epoch" "$(jget .status.bindingEpoch)" 1
	expect "phase" "$(jget .status.phase)" Running
	expect "Ready" "$(jget '.status.conditions[] | select(.type == "Ready") | .status')" True
fi
wait_for "garam's view of the agent to read ready" 120 state_is ready
cookie=$owner_token
call GET "$api/orgs/$org/agents/$agent_id"
cookie=
expect "garam's view after the operator's second report" "$(jget .provisioningState)" ready

# ---------------------------------------------------------------------
step 10 "the agent is stopped through the control service; the operator halts the MicroVM and deletes nothing"
stop_body=$(jq -nc --arg id "pair-run-stop-$suffix" '{requestId: $id}')
mint iam:agent.configure "$agent_grn" "pair-run-stop-$suffix" "$stop_body"
authorization=$authority
curl_extra=$control_curl
call POST "$control/v1/orgs/$org/agents/$(printf '%s' "$agent_grn" | sed 's|/|%2F|g')/stop" json "$stop_body"
authorization=
curl_extra=
expect "stop status" "$status" 200
expect "stopped" "$(jget .stopped)" true

curl_extra=$read_curl
halted() {
	curl -fsS $read_curl "$gaon/microvms/$name" | jq -e '.spec.runStrategy == "Halted"'
}
wait_for "the microvm to read Halted" 120 halted
call GET "$gaon/microvms/$name"
expect "microvm's run strategy" "$(jget .spec.runStrategy)" Halted
for object in "secrets/$name" "volumes/$name-memory" "volumes/$name-workspace"; do
	call GET "$gaon/$object"
	expect "$object is still there" "$status" 200
done
curl_extra=
if [ "$mode" = standin ]; then
	expect "DELETEs the stand-in served" "$(grep -c 'method=DELETE' "$tmp/standin.log" || true)" 0
else
	gaon_stopped() {
		curl -fsS $admin_curl "$gaon/microvms/$name" | jq -e '.status.phase != "Running"'
	}
	wait_for "the Node Agent to stop the MicroVM" 120 gaon_stopped
	curl_extra=$admin_curl
	call GET "$gaon/microvms/$name"
	curl_extra=
	say "the Node Agent stopped the MicroVM: phase $(jget .status.phase)"
	checked=$((checked + 1))
	say "  ok: the MicroVM is no longer Running once its run strategy is Halted"
fi

if [ "$mode" = gaon ]; then
	# ---------------------------------------------------------------
	step 11 "what gaon's RBAC refuses the operator, and what no service logged"
	curl_extra=$gaon_curl
	call GET "$gaon/microvms"
	expect "a list of MicroVMs with no selector is refused" "$status" 403
	call GET "$gaon/secrets"
	expect "a list of Secrets with no selector is refused" "$status" 403
	call GET "$gaon/secrets?labelSelector=garam.sh%2Frole%3Dother"
	expect "a list of Secrets labelled for something else is refused" "$status" 403
	call GET "$gaon/microvms/$name"
	expect "a get is refused: the role grants none" "$status" 403
	call GET "$gaon/nodes"
	expect "Nodes are not the operator's" "$status" 403
	call PUT "$gaon/secrets/not-the-operators" json "$(jq -nc '{apiVersion: "compute.gaon.garam.sh/v1alpha1", kind: "Secret",
		metadata: {name: "not-the-operators", labels: {"garam.sh/role": "other"}}, data: {a: "Yg=="}}')"
	expect "a Secret labelled for something else cannot be created" "$status" 403
	call PUT "$rbac/clusterroles/garam-gaon-operator-wider" json "$(jq -nc '{apiVersion: "rbac.gaon.garam.sh/v1alpha1", kind: "ClusterRole",
		metadata: {name: "garam-gaon-operator-wider"}, rules: [{apiGroups: ["*"], resources: ["*"], verbs: ["*"]}]}')"
	expect "the operator cannot register a role" "$status" 403
	call PUT "$rbac/clusterrolebindings/garam-gaon-operator-wider" json "$(jq -nc '{apiVersion: "rbac.gaon.garam.sh/v1alpha1",
		kind: "ClusterRoleBinding", metadata: {name: "garam-gaon-operator-wider"}, roleRef: {kind: "ClusterRole", name: "garam-gaon-operator"},
		subjects: [{kind: "User", name: "garam-gaon-operator"}]}')"
	expect "the operator cannot bind a role" "$status" 403
	curl_extra=
	# A watch: the role grants it, and the operator does not use it.
	# gaon wants the version to watch from, which a list answers.
	curl_extra=$gaon_curl
	call GET "$gaon/microvms?$agent_selector"
	curl_extra=
	rv=$(jget .metadata.resourceVersion)
	watch_status=$(curl -sS -m 3 -o /dev/null -w '%{http_code}' $gaon_curl "$gaon/microvms?watch=true&resourceVersion=$rv&$agent_selector" || true)
	expect "the role's watch of its MicroVMs is answered" "$watch_status" 200
	watch_status=$(curl -sS -m 3 -o /dev/null -w '%{http_code}' $gaon_curl "$gaon/microvms?watch=true&resourceVersion=$rv" || true)
	expect "a watch of every MicroVM is refused" "$watch_status" 403
	expect "the model-key Secret is not readable by name" "$(curl -sS -o /dev/null -w '%{http_code}' $gaon_curl "$gaon/secrets/model-keys")" 403

	# The harness copies each service's log out when it stops.
	kill "$harness_pid" 2>/dev/null || true
	wait "$harness_pid" 2>/dev/null || true
	harness_pid=
	for log in "$tmp"/gaonout/logs/*.log; do
		say "  $(basename "$log"): $(grep -ci forbidden "$log" || true) lines naming Forbidden"
	done
	forbidden=$(cat "$tmp"/gaonout/logs/*.log | grep -ci forbidden || true)
	say "(the controls above are refused by design; their refusals are the API server's, which logs them by status only if at all)"
	expect "no service of gaon logged Forbidden" "$forbidden" 0
	expect "the operator's log names no refusal" "$(grep -ci 'forbidden\|403' "$tmp/operator.log" || true)" 0
fi

echo
echo "pair-run: $checked checks passed at $commit$dirty (mode $mode)"
if [ "$mode" = standin ]; then
	echo "pair-run: seeded rather than done through a service: the owner's user and session (garam's test-principal fixture); the control service's profile (its own publish-profile subcommand); gaon's API state (the stand-in: the model-key Secret through its Secret route, the MicroVM's status through its admin path)"
else
	echo "pair-run: gaon commit $gaon_commit; seeded rather than done through a service: the owner's user and session (garam's test-principal fixture); the control service's profile (its own publish-profile subcommand); the model-key Secret, the operator's ClusterRole and its binding (put as gaon's administrator); the Node's admission (the harness, as the administrator)"
	echo "pair-run: the MicroVM is started by gaon's Node Agent against a minimal mvmd that runs nothing; no guest program ran"
fi
