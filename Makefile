# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
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

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup installs Kind into $(LOCALBIN) and builds/loads the Manager Docker image locally.
# kubectl kuberc is disabled by default for test isolation; enable with:
# - KUBECTL_KUBERC=true
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= garam-agent-operator-test-e2e

# The kubeconfig this run owns, and the destination of every command the suite
# makes: `go test` below carries it, and kubectl, kustomize and the sub-makes
# `make deploy` and `make undeploy` all inherit it from there. Given no
# --kubeconfig, kind writes $KUBECONFIG or ~/.kube/config and rewrites its
# current-context on every create and delete, so a run reading that file takes
# its destination from whatever another process last left in it — which is how a
# run aimed at Kind reached a production cluster (#111). Under dist/ because a
# target writes it and nothing tracks it.
KUBECONFIG_E2E := $(abspath dist/kubeconfig-$(KIND_CLUSTER))

.PHONY: setup-test-e2e
setup-test-e2e: kind ## Set up a Kind cluster for e2e tests if it does not exist
	@mkdir -p dist
	@case "$$("$(KIND)" get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Writing its kubeconfig to $(KUBECONFIG_E2E)."; \
			"$(KIND)" export kubeconfig --name $(KIND_CLUSTER) --kubeconfig "$(KUBECONFIG_E2E)" ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)' with kubeconfig $(KUBECONFIG_E2E)..."; \
			"$(KIND)" create cluster --name $(KIND_CLUSTER) --kubeconfig "$(KUBECONFIG_E2E)" ;; \
	esac

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	KUBECONFIG="$(KUBECONFIG_E2E)" KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: kind ## Tear down the Kind cluster used for e2e tests
	@"$(KIND)" delete cluster --name $(KIND_CLUSTER) --kubeconfig "$(KUBECONFIG_E2E)"
	@rm -f "$(KUBECONFIG_E2E)"

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
# linters run, not of which — dropping one of the other 19 leaves both numbers
# right.
.PHONY: lint-coverage
lint-coverage: golangci-lint ## Report the linters make lint runs and the tracked Go files it reaches, and fail when either covers nothing.
	@rules=$$("$(GOLANGCI_LINT)" linters | sed -n '/^Enabled by your configuration/,/^$$/p' | sed -n '/^Disabled/q; s/^\([^: ]*\): .*/\1/p'); \
	enabled=$$(printf '%s\n' "$$rules" | grep -c . || true); \
	printf '%s\n' "$$rules" | grep -qx misspell || { echo "lint coverage: misspell is not among the $$enabled linters golangci-lint reports enabled, so the plant below says nothing about what \`make lint\` runs" >&2; exit 1; }; \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	total=$$(git ls-files '*.go' | wc -l); \
	expected=$$(git ls-files '*.go' | xargs -r grep -L '^// Code generated .* DO NOT EDIT\.$$'); \
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

# The single name for the whole check set. CI invokes this target, not the
# commands inside it. `lint` runs before `fmt` so unformatted code fails the
# check instead of being rewritten and passing. `verify-pins` runs last because
# it is the one step that needs the network, and a contributor who cannot reach
# github.com cannot push what the rest of the set just cleared either.
.PHONY: ci
ci: lint-config lint fmt test build verify-pins ## Run the whole check set — lint, format, test, build.

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name garam-agent-operator-builder
	$(CONTAINER_TOOL) buildx use garam-agent-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm garam-agent-operator-builder
	rm Dockerfile.cross

# `kustomize edit set image` writes to the kustomization it runs in, and
# config/manager/kustomization.yaml is a tracked manifest: a target that edited it
# would hand the next one a dirty tree. This overlay carries the override instead,
# and it is build output — rebuilt from scratch on every run, so nothing stale
# survives a change to IMG.
IMAGE_OVERLAY := dist/image-overlay

.PHONY: image-overlay
image-overlay: kustomize ## Write the kustomize overlay that points the manager image at IMG.
	rm -rf $(IMAGE_OVERLAY)
	mkdir -p $(IMAGE_OVERLAY)
	cd $(IMAGE_OVERLAY) && "$(KUSTOMIZE)" create --resources ../../config/default
	cd $(IMAGE_OVERLAY) && "$(KUSTOMIZE)" edit set image controller=${IMG}

.PHONY: build-installer
build-installer: manifests generate kustomize image-overlay ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	"$(KUSTOMIZE)" build $(IMAGE_OVERLAY) > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

# A second `make uninstall` meets the CRDs the first one deleted, so absence is
# not a failure here. Only NotFound is ignored, and ignore-not-found on the
# command line still overrides this default.
.PHONY: uninstall
uninstall: ignore-not-found = true
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=false to fail when a CRD is already absent.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize image-overlay ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	"$(KUSTOMIZE)" build $(IMAGE_OVERLAY) | "$(KUBECTL)" apply -f -

# A second `make undeploy` meets the objects the first one deleted, so absence is
# not a failure here. Only NotFound is ignored, so a delete the cluster refuses
# still fails the target; a run that finds part of the set already gone also exits
# 0, and its output names what it deleted rather than what was missing.
.PHONY: undeploy
undeploy: ignore-not-found = true
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=false to fail when an object is already absent.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
# kubectl is not pinned the way the tools below are: k8s.io/kubernetes carries
# replace directives, which `go install <pkg>@<version>` refuses. A pin here
# would not reach the suite either — `test/e2e` and `test/utils` run `kubectl`
# directly, past this variable — so the version a run gets is whatever is on
# PATH, which Kubernetes supports only within one minor of the cluster kind
# creates.
KUBECTL ?= kubectl
KIND ?= $(LOCALBIN)/kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
# The PM moves the three pins below and reviews them when the Kubernetes
# libraries move. No bot proposes a bump: `.github/dependabot.yml` configures
# github-actions and docker, neither ecosystem reads a Makefile variable, and
# none of these tools is a dependency of this module.
KIND_VERSION ?= v0.33.0
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.21.0

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

# The PM moves this pin too, for the reason above. Nothing schedules the
# review: no Kubernetes release bears on it, so it moves when a Go release or a
# lint failure requires it.
GOLANGCI_LINT_VERSION ?= v2.12.2

.PHONY: kind
kind: $(KIND) ## Download kind locally if necessary.
$(KIND): $(LOCALBIN)
	$(call go-install-tool,$(KIND),sigs.k8s.io/kind,$(KIND_VERSION))

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

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

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
