# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker, which is
# the default. However, you might want to replace it to use other
# tools (i.e. podman).
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

# Every `go` invocation below builds with at least the toolchain this module
# targets. Left unset, `go install` picks the oldest toolchain satisfying the
# tool's own go directive, which can be older than go.mod targets — golangci-lint
# built that way then refuses to run against this module. Reading the floor from
# go.mod keeps the version written in one place, and `+auto` still lets a tool
# that needs a newer Go fetch one.
export GOTOOLCHAIN := go$(shell sed -n 's/^go //p' go.mod)+auto

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: fmt vet ## Run tests.
	go test ./... -coverprofile cover.out

# Builds cmd/control and runs it against a PostgreSQL container testcontainers-go
# starts on the Docker daemon DOCKER_HOST names; it needs no cluster. It ends by
# counting the top-level tests that passed, failed and skipped, because a run
# whose cases all skipped exits zero as the one that ran them does.
.PHONY: test-e2e-control
test-e2e-control: garam-e2e ## Run the control service's e2e tests: the built binary against PostgreSQL and garam at GARAM_REVISION.
	@echo "test-e2e-control: garam at $(GARAM_REVISION), from $$(cat "$(GARAM_DIR)/provenance")"
	@log=$$(mktemp); status=0; \
	GARAM_BIN_DIR="$(GARAM_DIR)" GARAM_MIGRATIONS_DIR="$(GARAM_DIR)/_migrations" \
		go test -tags=e2e ./tests/control/ -v -count=1 2>&1 | tee "$$log" || status=$$?; \
	echo "test-e2e-control: every test in ./tests/control with -tags=e2e:" \
		"$$(grep -c '^--- PASS' "$$log") passed, $$(grep -c '^--- FAIL' "$$log") failed," \
		"$$(grep -c '^--- SKIP' "$$log") skipped"; \
	rm -f "$$log"; exit $$status
# Every golangci-lint invocation below analyses against a cache scoped to this
# checkout. The default location is per-user, and each entry carries the
# absolute path the file was first seen at, so a second checkout holding
# identical content inherits results keyed to the first's paths -- which the
# `generated` and `nolint` exclusions then cannot open, and issues they would
# have filtered are reported instead (#71). `lint-coverage` overrides this with
# the temporary cache its probe needs.
export GOLANGCI_LINT_CACHE = $(LOCALBIN)/golangci-lint-cache

.PHONY: lint
lint: lint-coverage golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

# `0 issues.` reads the same whether the linter opened every file or none, and
# here it has already been the second: until #36 nothing behind `//go:build e2e`
# was ever read. This target plants a misspelling in every tracked Go file, in a
# copy of the tree, and requires the linter to report each one back — a file the
# config stops reaching fails the check instead of going quiet. Generated output
# is left out because `exclusions.generated` filters its findings by design.
# The flags lift the truncation that would keep 3 of the 14 identical findings,
# and run only the linter carrying the plant: reach is decided by the build
# tags, paths and exclusions, which all still apply, and a full analysis costs
# 17s against 0.2s for the same answer.
# That flag also overrides the `enable` list, so the plant says something about
# `make lint` only while that list still names misspell: requiring it is what
# fails a list emptied to nothing, which otherwise leaves this line and
# `0 issues.` reading exactly as they do on a healthy run (#50). The second
# `sed` stops the read at the disabled half of `linters`, which a lost blank
# line would otherwise let answer for the enabled half. The count is of how many
# linters run, not of which — dropping one of the other 17 leaves both numbers
# right.
.PHONY: lint-coverage
lint-coverage: golangci-lint ## Report the linters make lint runs and the tracked Go files it reaches, and fail when either covers nothing.
	@rules=$$("$(GOLANGCI_LINT)" linters | sed -n '/^Enabled by your configuration/,/^$$/p' | sed -n '/^Disabled/q; s/^\([^: ]*\): .*/\1/p'); \
	enabled=$$(printf '%s\n' "$$rules" | grep -c . || true); \
	printf '%s\n' "$$rules" | grep -qx misspell || { echo "lint coverage: misspell is not among the $$enabled linters golangci-lint reports enabled, so the plant below says nothing about what \`make lint\` runs" >&2; exit 1; }; \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	total=$$(git ls-files '*.go' | wc -l); \
	expected=$$(git ls-files '*.go' | xargs -r grep -L '^// Code generated .* DO NOT EDIT\.$$' || true); \
	[ -n "$$expected" ] || { echo "lint coverage: no tracked Go file to probe" >&2; exit 1; }; \
	git ls-files -z | tar --null -T - -cf - | tar -xf - -C "$$tmp"; \
	for f in $$expected; do printf '\n// lint coverage probe: recieve\n' >>"$$tmp/$$f"; done; \
	( cd "$$tmp" && GOLANGCI_LINT_CACHE="$$tmp/cache" "$(GOLANGCI_LINT)" run --enable-only misspell --max-same-issues=0 --max-issues-per-linter=0 >probe.out ) || true; \
	reached=0; missing=; \
	for f in $$expected; do \
		if grep -qE "(^|/)$$f:[0-9]+:[0-9]+: .*\(misspell\)$$" "$$tmp/probe.out"; \
		then reached=$$((reached+1)); else missing="$$missing $$f"; fi; \
	done; \
	[ -z "$$missing" ] || { echo "lint coverage: the linter reported nothing planted in:$$missing" >&2; exit 1; }; \
	echo "lint coverage: $$enabled linters enabled, $$reached of $$total tracked Go files reported a planted violation ($$((total-reached)) generated, not expected to)"

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Checks

# A `uses:` line states one fact twice: the SHA GitHub resolves and the version
# comment a human reads. Dependabot (`.github/dependabot.yml`) rewrites the two
# together only while they already agree (dependabot-core#7912), and rewrites the
# comment only while the version is the line's last word — `updated_version_comment`
# in dependabot-core's github_actions file updater returns early on a comment
# carrying anything after the version. Such a pin still resolves and still passes
# every other check while nothing moves its comment again, so both halves are
# required here: the version last, and the tag it names resolving to the SHA
# beside it. The second alone would catch the appended note only after the next
# bump, if ever.
# `git ls-remote`, not the REST API: no token, no API rate limit, and it is the
# transport a contributor already needs to push. A pin it cannot resolve fails —
# "could not ask" is not "agrees", and a run that skipped every pin would read
# exactly like one that compared them, which is what the two zero counts below
# refuse.
# The version pattern admits no glob character, and that is load-bearing:
# `git ls-remote https://github.com/actions/checkout 'refs/tags/v*'` answers with
# 72 refs, one of them the pinned SHA, so `# v*` passes any check that searches
# the answer instead of reading the ref it named. Reading the peeled `^{}` value
# first is the same guard for an annotated tag, whose unpeeled SHA is in the
# answer and is not what GitHub runs.
.PHONY: verify-pins
verify-pins: ## Report every SHA-pinned action and fail when its version comment is not the line's last word or does not resolve to its SHA.
	@shape='^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]+([^@[:space:]]+)@([0-9a-f]{40})[[:space:]]+#[[:space:]]*(v?[0-9][A-Za-z0-9._+-]*)$$'; \
	entries=$$(git grep -nE '^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]' -- '*.yml' '*.yaml' || true); \
	total=$$(printf '%s' "$$entries" | grep -c . || true); \
	[ "$$total" -gt 0 ] || { echo "pin check: no \`uses:\` line in any tracked YAML file, so nothing was checked" >&2; exit 1; }; \
	pins=; malformed=; \
	while IFS= read -r entry; do \
		site=$$(printf '%s' "$$entry" | cut -d: -f1,2); \
		line=$$(printf '%s' "$$entry" | cut -d: -f3- | sed 's/[[:space:]]*$$//'); \
		if [[ "$$line" =~ $$shape ]]; then \
			action="$${BASH_REMATCH[2]}"; rest="$${action#*/}"; \
			pins=$$(printf '%s\n%s/%s %s %s' "$$pins" "$${action%%/*}" "$${rest%%/*}" "$${BASH_REMATCH[4]}" "$${BASH_REMATCH[3]}"); \
		else \
			malformed=$$(printf '%s\n  %s: %s' "$$malformed" "$$site" "$$line"); \
		fi; \
	done <<<"$$entries"; \
	[ -z "$$malformed" ] || { printf 'pin check: not `<action>@<40-hex SHA> # <version>` with the version last, which is the only form Dependabot keeps rewriting:%s\n' "$$malformed" >&2; exit 1; }; \
	files=$$(printf '%s\n' "$$entries" | cut -d: -f1 | sort -u | grep -c .); \
	resolved=0; \
	while read -r repo tag sha; do \
		[ -n "$$repo" ] || continue; \
		refs=$$(git ls-remote "https://github.com/$$repo" "refs/tags/$$tag" "refs/tags/$$tag^{}") || { echo "pin check: could not reach https://github.com/$$repo to resolve $$tag" >&2; exit 1; }; \
		actual=$$(printf '%s\n' "$$refs" | awk -v t="refs/tags/$$tag^{}" '$$2==t{print $$1}'); \
		[ -n "$$actual" ] || actual=$$(printf '%s\n' "$$refs" | awk -v t="refs/tags/$$tag" '$$2==t{print $$1}'); \
		[ -n "$$actual" ] || { echo "pin check: $$repo has no tag $$tag, so the comment claiming it names nothing" >&2; exit 1; }; \
		[ "$$actual" = "$$sha" ] || { echo "pin check: $$repo $$tag is $$actual, but the line commented $$tag pins $$sha" >&2; exit 1; }; \
		resolved=$$((resolved+1)); \
	done <<<"$$(printf '%s\n' "$$pins" | sort -u | grep -v '^$$' || true)"; \
	[ "$$resolved" -gt 0 ] || { echo "pin check: $$total \`uses:\` lines and not one pin to resolve, so nothing was compared" >&2; exit 1; }; \
	echo "pin check: $$total action pins across $$files tracked YAML files, $$resolved distinct tags resolved, every version comment names the SHA beside it"

# go.mod's `go 1.26.0` admits `go mod tidy -diff` (added in Go 1.23): it prints
# what `go mod tidy` would change in go.mod and go.sum and exits non-zero, and
# writes neither, so the check leaves the tree as it found it whether it passes
# or fails. A non-zero exit with no diff is tidy failing to run (a module it
# could not fetch, say), which is reported as unchecked rather than as untidy.
# The status is taken with `||` because the recipe shell runs with -e.
.PHONY: verify-tidy
verify-tidy: ## Fail when go.mod or go.sum is not what `go mod tidy` writes, naming the fix.
	@status=0; diff=$$(go mod tidy -diff) || status=$$?; \
	if [ "$$status" -eq 0 ]; then echo "tidy check: go mod tidy -diff changes nothing in go.mod or go.sum"; exit 0; fi; \
	if [ -z "$$diff" ]; then echo "tidy check: go mod tidy -diff exited $$status without a diff, so tidiness was not checked" >&2; exit 1; fi; \
	printf '%s\n' "$$diff" >&2; \
	echo "tidy check: go.mod or go.sum is not tidy. Run \`go mod tidy\` and commit what it changes." >&2; exit 1

# The images build from the repository root through the one .dockerignore, which
# ignores everything and re-includes what the build needs. A file Go source embeds
# and the ignore file leaves out stops `go build` inside the image build only:
# make ci and the e2e suites build outside Docker, which is how #301 reached dev.
# This checks every //go:embed file of every package against the ignore file, read
# with the matcher BuildKit applies, without building an image.
.PHONY: verify-build-context
verify-build-context: ## Fail when .dockerignore leaves out a file Go source embeds, which an image build would then miss.
	go run ./hack/buildcontext

# The single name for the whole check set. CI invokes this target, not the
# commands inside it. `lint` runs before `fmt` so unformatted code fails the
# check instead of being rewritten and passing. `verify-tidy` runs first: it is
# quick, and an untidy module is a fault in the change, not in the code the rest
# of the set reads. `verify-pins` runs last because
# it is the one step that needs the network, and a contributor who cannot reach
# github.com cannot push what the rest of the set just cleared either.
.PHONY: ci
ci: verify-tidy lint-config lint fmt test build verify-build-context verify-pins ## Run the whole check set — module tidiness, lint, format, test, build, the image build context.

##@ Build

.PHONY: build
build: fmt vet ## Build the control, gaon operator, guest supervisor and render binaries.
	go build -o bin/control ./cmd/control
	go build -o bin/gaon-operator ./cmd/gaon-operator
	go build -o bin/gaon-guest ./cmd/gaon-guest
	go build -o bin/gaon-render ./cmd/gaon-render

# The control service's image, from build/control.Dockerfile with the repository root
# as context; REVISION is the checked-out commit, which the Dockerfile writes into the
# image's revision label.
CONTROL_IMG ?= control:latest

.PHONY: docker-build-control
docker-build-control: ## Build docker image with the control service.
	$(CONTAINER_TOOL) build --build-arg REVISION=$$(git rev-parse HEAD) -f build/control.Dockerfile -t ${CONTROL_IMG} .

# The images this project publishes, built for publishing and pushed by nothing
# here (delivery.md, ADR 0051, ADR 0084): the gaon operator's from
# build/operator.Dockerfile, the composed guest's from build/guest.Dockerfile and
# the control service's from build/control.Dockerfile, each one linux/amd64
# manifest, tagged locally garam-agent-operator/<image>:<REVISION> and labelled
# with REVISION. The release workflow and the manual procedure for a development
# image both build through this target, then tag and push what it built.
#
# The guest's three source images are pinned by digest in build/guest.Dockerfile
# and live in a private registry, so building it needs a login that can pull them,
# as `make garam-e2e` needs one for garam's test image. release.yml logs in before
# it builds; a person without that login builds the other two with `make
# operator-image` and `make control-image`.
#
# The default driver writes each image into the local image store, where `docker
# push` finds it. BUILDX_NO_DEFAULT_ATTESTATIONS turns off the provenance and SBOM
# attestations buildx attaches by default, which are carried in an index over the
# image, so the digest a deployment pins names the image itself. It is set in the
# environment rather than as `--provenance=false --sbom=false` because a builder
# that does not know it ignores it, where the flags fail the build — the reasoning
# sherlock@b3c05c2:Makefile records for its own target.
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null)
IMAGE_PLATFORM = linux/amd64

.PHONY: build-images
build-images: operator-image guest-image control-image ## Build the three images to publish, each one linux/amd64 manifest, and push nothing. The guest needs a login that pulls its three pinned source images.

.PHONY: operator-image
operator-image: ## Build the gaon operator's image, one linux/amd64 manifest, and push nothing.
	@test -n "$(REVISION)" || { echo "operator-image: REVISION is empty; pass one: make operator-image REVISION=<commit>"; exit 1; }
	BUILDX_NO_DEFAULT_ATTESTATIONS=1 $(CONTAINER_TOOL) build --platform=$(IMAGE_PLATFORM) --build-arg IMAGE_REVISION="$(REVISION)" --build-arg IMAGE_VERSION="$(REVISION)" -f build/operator.Dockerfile -t garam-agent-operator/operator:$(REVISION) .

.PHONY: guest-image
guest-image: ## Build the composed guest image, one linux/amd64 manifest, and push nothing. Needs a login that pulls its three pinned source images.
	@test -n "$(REVISION)" || { echo "guest-image: REVISION is empty; pass one: make guest-image REVISION=<commit>"; exit 1; }
	BUILDX_NO_DEFAULT_ATTESTATIONS=1 $(CONTAINER_TOOL) build --platform=$(IMAGE_PLATFORM) --build-arg REVISION="$(REVISION)" -f build/guest.Dockerfile -t garam-agent-operator/guest:$(REVISION) .

.PHONY: control-image
control-image: ## Build the control service's image, one linux/amd64 manifest, and push nothing.
	@test -n "$(REVISION)" || { echo "control-image: REVISION is empty; pass one: make control-image REVISION=<commit>"; exit 1; }
	BUILDX_NO_DEFAULT_ATTESTATIONS=1 $(CONTAINER_TOOL) build --platform=$(IMAGE_PLATFORM) --build-arg REVISION="$(REVISION)" -f build/control.Dockerfile -t garam-agent-operator/control:$(REVISION) .

# What only the built images can break; neither is part of ci, because each
# needs a Docker daemon, and guest-run a login for the pinned source images.
.PHONY: operator-image-check
operator-image-check: operator-image ## Check the gaon operator's image runs as a numeric non-root account, ships no shell and names its missing settings.
	GAON_OPERATOR_IMAGE=garam-agent-operator/operator:$(REVISION) sh tests/gaon/operator-image.sh

.PHONY: guest-run
guest-run: guest-image ## Run the composed guest image as one container and check the supervisor's start order, readiness port and drain.
	sh tests/gaon/guest-image.sh garam-agent-operator/guest:$(REVISION)

# The gaon operator against a real garam, at GARAM_REVISION and run from garam-e2e's
# files, and a real control service built from this tree, with tests/gaon/gaonstandin
# for gaon's API. It needs a Docker daemon (one PostgreSQL container), openssl, jq,
# curl, python3 and the Go toolchain, prints each check, and exits non-zero where an
# answer is not the one it names. Name the compose project with PAIR_RUN_PROJECT.
# Not part of ci.
.PHONY: pair-run
pair-run: garam-e2e ## Run the gaon operator against garam and the control service, with a stand-in for gaon's API.
	GARAM_DIR="$(GARAM_DIR)" sh tests/pairrun/pair-run.sh

# The same run against gaon's own built services in place of the stand-in, with
# authorization on. GAON_REPO names a gaon checkout, which is cloned and not
# changed, and GAON_COMMIT the commit built from it; the commit moves with the
# github.com/garamsh/gaon/lib/api pin in go.mod, to the same commit of gaon, in the
# same pull request. It needs the module cache to hold gaon's etcd and mvmd's
# runtime API (tests/pairrun/gaon-build.sh). Not part of ci.
GAON_COMMIT ?= 2387cebd0b44b322b7da2ff485144a7456a97c6f

.PHONY: pair-run-gaon
pair-run-gaon: garam-e2e ## Run the same pair run against gaon's own built services. Needs GAON_REPO=<gaon checkout>.
	@test -n "$(GAON_REPO)" || { echo "pair-run-gaon: GAON_REPO names the gaon checkout to clone" >&2; exit 1; }
	GARAM_DIR="$(GARAM_DIR)" PAIR_RUN_GAON=gaon GAON_REPO="$(GAON_REPO)" GAON_COMMIT="$(GAON_COMMIT)" sh tests/pairrun/pair-run.sh

# Refuses IMAGE_REF where ADR 0078's tag rule forbids its push: under
# garam-agent-operator/, a release version tag (0.1.0-dev.N, 0.1.0-rc.N or 0.1.0, ADR 0081)
# outside release.yml's run for that version, and any tag but those or the 12-hex commit
# hash (#401). Every target that pushes runs it
# first, and so does release.yml. It stops mistakes, not a determined pusher, since a
# local shell can export GITHUB_WORKFLOW_REF; IAM stops the rest.
.PHONY: verify-image-ref
verify-image-ref: ## Refuse an IMAGE_REF under garam-agent-operator/ unless it is the 12-hex commit hash, or a release version (0.1.0-dev.N, 0.1.0-rc.N, 0.1.0) in its release run.
	go run ./hack/imageref "$(IMAGE_REF)"
##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

# The garam the control e2e suite runs against, at GARAM_REVISION, in a
# directory named for it: `garam`, the test-principal fixture that prepares its
# database (tests/testprincipal/README.md §Invocation at that commit), and its
# migrations, as `_migrations/`: the leading underscore keeps the go tool's
# `./...` out of garam's migration tests there. They are copied out of garam's
# private test image in ECR, in account 486152169996, pinned by digest, and
# never run from it (#271). The image carries the commit it was built from as
# its org.opencontainers.image.revision label, and garam-e2e refuses an image
# whose label is not GARAM_REVISION. Reading it needs an ECR login that can pull
# from that repository, such as `aws ecr get-login-password | docker login`.
#
# The image is bound by digest alone. Its tags, the commit's 12 hex digits
# (43d700d30910) and pinned-garam-agent-operator, are informational; garam's
# lifecycle policy keeps it while pinned-garam-agent-operator names it
# (garamsh/garam@43d700d3 platform/tests/testprincipal/README.md §The test image).
#
# Pinning (#271): GARAM_REVISION, GARAM_TEST_REPOSITORY and
# GARAM_TEST_IMAGE_DIGEST move together, in one pull request, by hand; no
# Dependabot ecosystem reads a Makefile variable. That pull request writes here
# the commit and digest garam's PM confirms, and is merged only once they are
# confirmed.
GARAM_REVISION ?= 43d700d30910319095b7f97230bffc79c2328981
GARAM_TEST_REGISTRY ?= 486152169996.dkr.ecr.ap-northeast-2.amazonaws.com
GARAM_TEST_REPOSITORY ?= garam/test
GARAM_TEST_IMAGE_DIGEST ?= sha256:5e9366ac0a5c0fed35f8726c63a962499d559961ab687a146ccc88aec122a2ce
GARAM_REPOSITORY ?= https://github.com/garamsh/garam.git
GARAM_DIR = $(LOCALBIN)/garam-$(GARAM_REVISION)

.PHONY: garam-e2e
garam-e2e: $(GARAM_DIR)/garam ## Copy garam, its test-principal fixture and its migrations at GARAM_REVISION out of garam's test image.

# Copied into a scratch directory that takes GARAM_DIR's name only once every
# file is in, so an interrupted copy never leaves the target looking made.
$(GARAM_DIR)/garam:
	@if [ -z "$(GARAM_TEST_REPOSITORY)" ] || [ -z "$(GARAM_TEST_IMAGE_DIGEST)" ]; then \
		echo "garam-e2e: GARAM_TEST_REPOSITORY or GARAM_TEST_IMAGE_DIGEST is empty: set both, or run make garam-e2e-from-source" >&2; exit 1; \
	fi; \
	ref="$(GARAM_TEST_REGISTRY)/$(GARAM_TEST_REPOSITORY)@$(GARAM_TEST_IMAGE_DIGEST)"; \
	$(CONTAINER_TOOL) pull --quiet "$$ref" >/dev/null || { echo "garam-e2e: could not pull $$ref: log in to $(GARAM_TEST_REGISTRY) with pull access to the repository, or run make garam-e2e-from-source" >&2; exit 1; }; \
	revision=$$($(CONTAINER_TOOL) image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$$ref"); \
	if [ "$$revision" != "$(GARAM_REVISION)" ]; then \
		echo "garam-e2e: $$ref is labelled revision '$$revision', but GARAM_REVISION is '$(GARAM_REVISION)': move the two together" >&2; exit 1; \
	fi; \
	rm -rf "$(GARAM_DIR)" "$(GARAM_DIR).partial" && mkdir -p "$(GARAM_DIR).partial"; \
	container=$$($(CONTAINER_TOOL) create "$$ref" /garam) || exit 1; \
	status=0; \
	for pair in /garam:garam /testprincipal:testprincipal /migrations:_migrations; do \
		$(CONTAINER_TOOL) cp "$$container:$${pair%%:*}" "$(GARAM_DIR).partial/$${pair#*:}" || status=1; \
	done; \
	$(CONTAINER_TOOL) rm "$$container" >/dev/null; \
	[ "$$status" -eq 0 ] || { echo "garam-e2e: could not copy garam's files out of $$ref" >&2; exit 1; }; \
	echo "image $$ref" > "$(GARAM_DIR).partial/provenance"; \
	mv "$(GARAM_DIR).partial" "$(GARAM_DIR)"; \
	echo "garam-e2e: garam, testprincipal and _migrations/ at $(GARAM_REVISION), from $$ref"

# The fallback for when garam's test image cannot be pulled: the same files built
# from garamsh/garam's source at GARAM_REVISION, which needs git credentials that
# can read that private repository. CI does not run it, because its token reads
# no other private repository (#271). It writes the layout garam-e2e does, so
# `make garam-e2e-from-source test-e2e-control` runs the suite on it.
.PHONY: garam-e2e-from-source
garam-e2e-from-source: ## Build garam, its test-principal fixture and its migrations at GARAM_REVISION from source, where the test image cannot be pulled.
	rm -rf "$(GARAM_DIR)" "$(GARAM_DIR).partial" && mkdir -p "$(GARAM_DIR).partial/src"
	git -C "$(GARAM_DIR).partial/src" init --quiet
	git -C "$(GARAM_DIR).partial/src" fetch --quiet --depth 1 "$(GARAM_REPOSITORY)" $(GARAM_REVISION)
	git -C "$(GARAM_DIR).partial/src" -c advice.detachedHead=false checkout --quiet FETCH_HEAD
	cd "$(GARAM_DIR).partial/src/platform" && go build -o ../../testprincipal ./tests/testprincipal && go build -o ../../garam ./cmd/garam
	cp -R "$(GARAM_DIR).partial/src/platform/migrations" "$(GARAM_DIR).partial/_migrations"
	echo "source $(GARAM_REPOSITORY)@$(GARAM_REVISION)" > "$(GARAM_DIR).partial/provenance"
	mv "$(GARAM_DIR).partial" "$(GARAM_DIR)"

GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

# The PM moves this pin. No bot proposes a bump: `.github/dependabot.yml`
# configures github-actions, docker and gomod, none of them reads a Makefile
# variable, and the linter is not a dependency of this module. It moves when a
# Go release or a lint failure requires it.
GOLANGCI_LINT_VERSION ?= v2.15.0

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
