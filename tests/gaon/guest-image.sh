#!/bin/sh
# guest-image.sh runs the composed guest image as one container, the way a
# MicroVM would run it (ADR-0082): the command, the environment and the
# credential files are what the operator's render.go renders for one
# agent, written out by cmd/gaon-render and not typed here; user 0:0.
# Where the render changes, this run changes with it. It shows the
# three programs starting in order, the readiness port answering, each
# program's account and capabilities, a command run as 10001 refused the
# store, the outbox, the credentials and the runtime's environment, and
# a SIGTERM drain in start order. It fails where one differs.
#
# SYS_ADMIN is added because the container's bounding set must hold the
# workspace's five capabilities, as a MicroVM's root does.
#
# The credentials are placeholders: a self-signed root, fixed token text
# for the two garam minted, a placement token the render mints, and the
# model credential from a file standing for the deployment's Secret. The
# adapter runs but no call it makes reaches garam. What it does with an
# admitted agent, and a real Exec, are not shown here.
#
# usage: guest-image.sh <image>
set -eu

image="${1:?usage: guest-image.sh <image>}"
name="${GUEST_DEMO_NAME:-gaon-guest-demo-$$}"
grn=grn:demo/iam/agent/0000000000000001
model_env=MODEL_API_KEY
model_key=demo-model-credential
ego='You are the demo agent of guest-image.sh.'
scratch="$(mktemp -d)"
here="$(cd "$(dirname "$0")/../.." && pwd)"

remove_container() { docker rm -f -v "$name" >/dev/null 2>&1 || true; }
cleanup() {
	remove_container
	rm -rf "$scratch"
}
trap cleanup EXIT
remove_container

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=demo \
	-keyout "$scratch/key.pem" -out "$scratch/root.pem" 2>/dev/null
printf '%s' "$model_key" >"$scratch/model-key"
mkdir "$scratch/render"
(cd "$here" && go run ./cmd/gaon-render \
	-image "$image" -agent "$grn" -epoch 1 \
	-machine-url https://127.0.0.1:9 -control-url https://127.0.0.1:9 \
	-provider mock -model-url http://127.0.0.1:9 -model-name demo \
	-model-ref model-credentials/api-key -model-key-file "$scratch/model-key" \
	-ego "$ego" \
	-root-file "$scratch/root.pem" -out "$scratch/render")
[ "$(wc -l <"$scratch/render/modes")" = 1 ] || { echo "the render gives the files more than one mode and owner"; exit 1; }
IFS=: read -r file_mode file_uid file_gid <"$scratch/render/modes"
credential_files="$(ls "$scratch/render/files")"
echo "== rendered: command $(cat "$scratch/render/entrypoint"), env $(cut -d= -f1 "$scratch/render/env.list" | tr '\n' ' ')"
echo "== rendered files: $(echo $credential_files)"

echo "== image labels"
docker image inspect "$image" --format \
	'{{range $k, $v := .Config.Labels}}{{$k}}={{$v}}{{"\n"}}{{end}}' | sort

docker create --name "$name" --user 0:0 --cap-add SYS_ADMIN \
	--entrypoint "$(cat "$scratch/render/entrypoint")" \
	--env-file "$scratch/render/env.list" \
	"$image" >/dev/null
# Named entries, not ".": the directory's own mode stays the image's.
# shellcheck disable=SC2086
tar -C "$scratch/render/files" --owner="$file_uid" --group="$file_gid" --mode="0$file_mode" -cf - \
	$credential_files | docker cp - "$name":/run/garam
docker start "$name" >/dev/null

echo "== readiness: waiting for port 8088 to answer"
tries=0
until docker exec "$name" bash -c 'exec 3<>/dev/tcp/127.0.0.1/8088' 2>/dev/null; do
	tries=$((tries + 1))
	[ "$tries" -lt 60 ] || { echo "port 8088 never answered"; docker logs "$name"; exit 1; }
	sleep 0.5
done
echo "port 8088 answers"

echo "== start order (supervisor log)"
docker logs "$name" 2>&1 | grep -E 'msg=(started|ready)' | cut -c1-120
started="$(docker logs "$name" 2>&1 | sed -n 's/.* msg=started program=\([a-z]*\) .*/\1/p' | tr '\n' ' ')"

failed=0
fail() { echo "FAIL: $*"; failed=1; }

pid_of() {
	docker logs "$name" 2>&1 | sed -n "s/.* msg=started program=$1 pid=\([0-9]*\).*/\1/p"
}
field() { # field <program> <status field>
	docker exec "$name" sed -n "s/^$2:[[:space:]]*//p" "/proc/$(pid_of "$1")/status"
}

echo "== each program's account"
for program in runtime workspace adapter; do
	echo "$program uid=$(field $program Uid | cut -f1) groups=[$(field $program Groups)] CapEff=$(field $program CapEff)"
done
[ "$(field runtime Uid | cut -f1)" = 65532 ] || fail "the runtime is not 65532"
[ "$(field adapter Uid | cut -f1)" = 65532 ] || fail "the adapter is not 65532"
[ "$(field workspace Uid | cut -f1)" = 0 ] || fail "the workspace is not root"
[ "$(field runtime CapEff)" = 0000000000000000 ] || fail "the runtime holds a capability"
[ "$(field adapter CapEff)" = 0000000000000000 ] || fail "the adapter holds a capability"
[ "$(field workspace CapEff)" = 00000000002000e1 ] || fail "the workspace does not hold exactly the runner's five"

echo "== the model credential reaches the runtime alone"
# Each program's environment is read as the account that owns it: a
# process of another account refuses it even to root without
# CAP_SYS_PTRACE.
for program in runtime workspace adapter; do
	if docker exec --user "$(field $program Uid | cut -f1)" "$name" grep -qa "$model_env=$model_key" "/proc/$(pid_of $program)/environ"; then
		echo "$program: has it"
		[ "$program" = runtime ] || fail "$program holds the model credential"
	else
		echo "$program: does not"
		[ "$program" != runtime ] || fail "the runtime does not hold the model credential"
	fi
done

echo "== the runtime reads the rendered configuration and ego"
# The files are inline content in the MicroVM, written by gaon as the
# rendered owner and mode. The runtime is given their paths on its
# command line, 65532 reads what they hold, and it is still running: a
# file it could not parse would have ended it before the readiness port
# answered.
runtime_cmd="$(docker exec "$name" sh -c "tr '\\0' ' ' </proc/$(pid_of runtime)/cmdline")"
echo "runtime command: $runtime_cmd"
case "$runtime_cmd" in *"--config /run/garam/runtime-config.yaml"*) ;; *) fail "the runtime is not given the configuration file" ;; esac
case "$runtime_cmd" in *"--ego-file /run/garam/runtime-ego.md"*) ;; *) fail "the runtime is not given the ego file" ;; esac
docker exec --user 65532:65532 "$name" cat /run/garam/runtime-config.yaml
[ "$(docker exec --user 65532:65532 "$name" cat /run/garam/runtime-ego.md)" = "$ego" ] || fail "the ego the runtime reads is not the rendered one"
docker exec --user 65532:65532 "$name" grep -q "api-key-env: $model_env" /run/garam/runtime-config.yaml || fail "the configuration does not name $model_env"
if docker exec "$name" grep -q "$model_key" /run/garam/runtime-config.yaml; then fail "the model key is in the configuration file"; fi
docker exec "$name" test -d "/proc/$(pid_of runtime)" || fail "the runtime is not running"

echo "== store and outbox are 65532's alone, mode 0700"
docker exec "$name" stat -c '%n %U:%G %a' /var/lib/garam/memory /var/lib/garam/memory/outbox
[ "$(docker exec "$name" stat -c '%u:%g:%a' /var/lib/garam/memory)" = 65532:65532:700 ] || fail "the store is not 0700 owned by 65532"
[ "$(docker exec "$name" stat -c '%u:%g:%a' /var/lib/garam/memory/outbox)" = 65532:65532:700 ] || fail "the outbox is not 0700 owned by 65532"
docker exec "$name" sh -c 'stat -c "%n %U:%G %a" /run/garam/*'
[ "$(docker exec "$name" sh -c 'stat -c "%u:%g:%a" /run/garam/* | sort -u')" = "$file_uid:$file_gid:$file_mode" ] || fail "a credential is not 0400 owned by 65532"

echo "== as 10001, the executor's child: each is refused"
refused() { # refused <description> <command...>
	description="$1"
	shift
	if docker exec --user 10001:10001 "$name" "$@" >/dev/null 2>&1; then
		fail "10001 may $description"
	else
		echo "refused: $description"
	fi
}
refused "list the store" ls /var/lib/garam/memory
refused "list the outbox" ls /var/lib/garam/memory/outbox
refused "write into the store" touch /var/lib/garam/memory/x
for f in $credential_files; do
	refused "read /run/garam/$f" cat "/run/garam/$f"
done
refused "read the runtime's environment" cat "/proc/$(pid_of runtime)/environ"
refused "read the adapter's environment" cat "/proc/$(pid_of adapter)/environ"
echo "== the same reads as 65532 succeed, so each refusal above is a permission"
docker exec --user 65532:65532 "$name" ls /var/lib/garam/memory >/dev/null || fail "65532 cannot list the store"
docker exec --user 65532:65532 "$name" cat /run/garam/agent-token >/dev/null || fail "65532 cannot read its token"
[ "$(docker exec --user 65532:65532 "$name" sh -c 'wc -c </run/garam/placement-token')" = 64 ] || fail "the placement token is not 64 hex digits"
echo "65532 lists the store and reads its token"

echo "== SIGTERM: docker stop sends it to the supervisor, which is PID 1"
docker stop -t 60 "$name" >/dev/null
docker logs "$name" 2>&1 | grep -E 'msg=(draining|terminating|exited)' | cut -c1-120
drained="$(docker logs "$name" 2>&1 | sed -n 's/.* msg=terminating program=\([a-z]*\)$/\1/p' | tr '\n' ' ')"
code="$(docker inspect "$name" --format '{{.State.ExitCode}}')"
echo "supervisor exit status: $code"

echo "== the MicroVM's user: $(docker inspect "$name" --format '{{.Config.User}}')"

[ "$started" = "runtime workspace adapter " ] || fail "start order was: $started"
[ "$drained" = "runtime workspace adapter " ] || fail "drain order was: $drained"
[ "$code" = 0 ] || fail "supervisor exited $code"
[ "$failed" = 0 ] || { echo "== FAILED"; exit 1; }
echo "== ok: started and drained runtime, workspace, adapter; 10001 refused"
