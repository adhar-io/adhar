LD_FLAGS=-ldflags " \
    -X adhar-io/adhar/cmd/version.Version=$(VERSION) \
    -X adhar-io/adhar/cmd/version.GitCommit=$(shell git rev-parse --short HEAD) \
    -X adhar-io/adhar/cmd/version.BuildDate=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ') \
    "

# Image URL to use all building/pushing image targets
IMG ?= adhar:latest
# Single source of truth for the platform version: the latest git tag. Used for
# both the binary ldflags and the control-plane package name, so `make build`
# and a GoReleaser release stamp the same version everywhere. Falls back to
# v0.1.0 on tagless checkouts (fresh forks, shallow clones).
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo v0.1.0)

# RELEASE_VERSION is the version `make release` will tag — a NEW version, NOT the
# auto-detected latest tag above (which is what `release` used to re-tag, causing
# "Tag vX.Y.Z already exists"). It is taken from, in order:
#   1. a positional goal:      make release v0.2.2
#   2. an explicit override:   make release VERSION=v0.2.2
RELEASE_VERSION := $(strip $(filter v%,$(MAKECMDGOALS)))
ifeq ($(RELEASE_VERSION),)
ifeq ($(origin VERSION),command line)
RELEASE_VERSION := $(VERSION)
endif
endif

# Swallow a positional `vX.Y.Z` goal so `make release v0.2.2` doesn't fail with
# "No rule to make target 'v0.2.2'". Explicit targets (vet, etc.) still win, so
# this only ever matches a version argument.
v%:
	@:

# The name of the binary. Defaults to adhar
OUT_FILE ?= adhar

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
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./api/..." output:crd:artifacts:config=platform/controllers/resources

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
.PHONY: e2e
e2e: build ## Run the e2e tests. Creates (and destroys) a local Kind cluster named 'adhar' via `adhar up`.
	@docker info >/dev/null 2>&1 || { \
		echo "Docker is not running. The e2e tests bootstrap a Kind cluster and need Docker."; \
		exit 1; \
	}
	go test -v -p 1 -timeout 120m --tags=e2e ./tests/e2e/...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet build-control-plane preload-images ## Build adhar binary and control-plane package (also warms the image cache for a fast `adhar up`).
	go build $(LD_FLAGS) -o $(OUT_FILE) ./cmd

.PHONY: build-control-plane
build-control-plane: ## Build Crossplane control-plane configuration package
	@echo "› Building Crossplane control-plane package..."
	@# A Crossplane v2 Configuration package contains only the meta (crossplane.yaml),
	@# XRDs and Compositions. ProviderConfigs, Function CRs and Operations are runtime
	@# resources (applied by the controller from configuration/), not package contents.
	@# Output goes to the gitignored platform/controlplane/dist/ — the package is
	@# a versioned release artifact (uploaded by GoReleaser), not a tracked file;
	@# the controller applies the embedded configuration/ tree directly.
	@mkdir -p platform/controlplane/dist
	@rm -f platform/controlplane/dist/adhar-control-plane-*.xpkg
	@if command -v crossplane >/dev/null 2>&1; then \
		mkdir -p platform/controlplane/dist/examples; \
		crossplane xpkg build \
			--package-root=platform/controlplane/configuration \
			--examples-root=platform/controlplane/dist/examples \
			--ignore="providers/*,providers/config/*,providers/cloud/*,functions/*,operations/*" \
			-o platform/controlplane/dist/adhar-control-plane-$(VERSION).xpkg; \
		rm -rf platform/controlplane/dist/examples; \
	else \
		echo "  crossplane CLI not found; falling back to tarball bundle"; \
		tar -czf platform/controlplane/dist/adhar-control-plane-$(VERSION).xpkg -C platform/controlplane/configuration .; \
	fi
	@echo "✓ Control-plane package ready: platform/controlplane/dist/adhar-control-plane-$(VERSION).xpkg"

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd

# Platform bootstrap + core-component images `adhar up` needs early: the Cilium
# critical path plus the heavy components that gate the rest (ArgoCD, Gitea, CNPG,
# ESO, Vault, Keycloak, kube-prometheus, Hubble relay/UI). Warming the host cache
# once lets every `adhar up` load them into the Kind node instead of pulling from
# the internet. Keep in sync with platform/providers/kind/preload.go.
ADHAR_PRELOAD_IMAGES ?= \
	quay.io/cilium/cilium:v1.20.0 \
	quay.io/cilium/cilium-envoy:v1.37.5-1782911245-7cffc778c923f68a77954a53b1a98d6b5353f004 \
	quay.io/cilium/operator-generic:v1.20.0 \
	ghcr.io/adhar-io/adhar-console:latest \
	busybox:1.36 \
	quay.io/argoproj/argocd:v3.5.1 \
	docker.gitea.com/gitea:1.27.0-rootless \
	docker.io/bitnami/postgresql:latest \
	docker.io/bitnami/valkey:latest \
	ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0 \
	ghcr.io/cloudnative-pg/postgresql:16-bookworm \
	ghcr.io/external-secrets/external-secrets:v2.5.0 \
	hashicorp/vault:1.21.2 \
	hashicorp/vault-k8s:1.7.2 \
	quay.io/keycloak/keycloak:26.7.1 \
	docker.io/grafana/grafana:13.1.3 \
	quay.io/prometheus/prometheus:v3.4.0 \
	quay.io/prometheus-operator/prometheus-operator:v0.93.0 \
	quay.io/prometheus/alertmanager:v0.28.1 \
	quay.io/prometheus/node-exporter:v1.12.1-distroless \
	registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1 \
	quay.io/cilium/hubble-relay:v1.20.0 \
	quay.io/cilium/hubble-ui:v0.13.5 \
	quay.io/cilium/hubble-ui-backend:v0.13.5

.PHONY: preload-images
preload-images: ## Pre-pull platform bootstrap + core images into the host Docker cache so `adhar up` loads them into the Kind node instead of pulling (much faster platform readiness). Runs as part of `make build`; parallel pulls; set ADHAR_SKIP_PRELOAD=1 to skip.
	@if [ -n "$(ADHAR_SKIP_PRELOAD)" ]; then \
		echo "preload-images: skipped (ADHAR_SKIP_PRELOAD set)"; \
	elif ! docker info >/dev/null 2>&1; then \
		echo "preload-images: Docker not available — skipping cache warm (adhar up will pull images in-cluster)"; \
	else \
		echo "preload-images: warming host Docker cache ($(words $(ADHAR_PRELOAD_IMAGES)) images, parallel)…"; \
		echo "$(ADHAR_PRELOAD_IMAGES)" | tr ' ' '\n' | grep . | xargs -P 4 -I{} sh -c 'docker pull "{}" >/dev/null 2>&1 && echo "  ok  {}" || echo "  skip {}"'; \
		echo "Host cache warmed. 'adhar up' will preload these into the Kind node."; \
	fi

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(shell git rev-parse --short HEAD) \
		--build-arg BUILD_DATE=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ') \
		-t ${IMG} .

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
	- $(CONTAINER_TOOL) buildx create --name adhar-builder
	$(CONTAINER_TOOL) buildx use adhar-builder
	- $(CONTAINER_TOOL) buildx build \
		--push \
		--platform=$(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(shell git rev-parse --short HEAD) \
		--build-arg BUILD_DATE=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ') \
		--tag ${IMG} \
		-f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm adhar-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image adhar=${IMG}
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Release Management

.PHONY: release
release: ## Create and push a release tag (e.g. make release v0.2.2); CI (GoReleaser) builds and publishes everything
	@REL="$(RELEASE_VERSION)"; \
	if [ -z "$$REL" ]; then \
		echo "Error: specify the version to release, e.g. 'make release v0.2.2'"; \
		echo "       (latest tag is $$(git describe --tags --abbrev=0 2>/dev/null || echo none))"; \
		exit 1; \
	fi; \
	case "$$REL" in \
		v[0-9]*.[0-9]*.[0-9]*) ;; \
		*) echo "Error: version must look like vX.Y.Z (got '$$REL')"; exit 1;; \
	esac; \
	git fetch --force --tags >/dev/null 2>&1 || true; \
	if git rev-parse "$$REL" >/dev/null 2>&1; then \
		echo "Error: Tag $$REL already exists"; exit 1; \
	fi; \
	git tag -a "$$REL" -m "Release $$REL"; \
	git push origin "$$REL"; \
	echo "Tag $$REL pushed. The release workflow now builds and publishes the release:"; \
	echo "  https://github.com/adhar-io/adhar/actions/workflows/release.yaml"

.PHONY: release-snapshot
release-snapshot: goreleaser ## Build a local snapshot release with GoReleaser (nothing is tagged or published)
	@$(GORELEASER) release --snapshot --clean --skip=docker
	@echo "Snapshot artifacts are in dist/"

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image adhar=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
HELM ?= $(LOCALBIN)/helm

# GoReleaser
GORELEASER ?= $(LOCALBIN)/goreleaser
GORELEASER_VERSION ?= v2.8.2

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.17.2
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= latest
HELM_VERSION ?= v4.2.4

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
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: embedded-resources
embedded-resources: kustomize helm
	export PATH=$(LOCALBIN):$$PATH; ./hack/embedded-resources.sh;

.PHONY: clean-control-plane
clean-control-plane: ## Clean control-plane build artifacts
	@rm -rf platform/controlplane/dist
	@echo "✓ Control-plane build artifacts cleaned"

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
# Built with the exact Go toolchain this module targets (from go.mod) so the
# linter's Go language version matches; builds with an older Go refuse to lint
# modules whose go directive is newer than the Go used to build the linter.
MODULE_GO_VERSION = $(shell awk '/^go /{print $$2}' go.mod)
$(GOLANGCI_LINT): $(LOCALBIN)
	@[ -f "$(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION)" ] || { \
	set -e; \
	echo "Building golangci-lint $(GOLANGCI_LINT_VERSION) with Go $(MODULE_GO_VERSION)"; \
	rm -f $(GOLANGCI_LINT) || true; \
	GOTOOLCHAIN=go$(MODULE_GO_VERSION) GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	mv $(GOLANGCI_LINT) $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION); \
	} ;\
	ln -sf $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION) $(GOLANGCI_LINT)

.PHONY: helm
helm: $(HELM) ## Download helm locally if necessary.
$(HELM): $(LOCALBIN)
	$(call go-install-tool,$(HELM),helm.sh/helm/v4/cmd/helm,$(HELM_VERSION))

.PHONY: goreleaser
goreleaser: $(GORELEASER) ## Download goreleaser locally if necessary.
$(GORELEASER): $(LOCALBIN)
	$(call go-install-tool,$(GORELEASER),github.com/goreleaser/goreleaser/v2,$(GORELEASER_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef
