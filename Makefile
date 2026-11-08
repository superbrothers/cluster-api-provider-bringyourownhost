# Ensure Make is run with bash shell as some syntax below is bash-specific
SHELL:=/usr/bin/env bash

# The registry of the image of the manager
REGISTRY ?= ghcr.io/superbrothers

IMAGE_NAME ?= cluster-api-byoh-controller
TAG ?= dev
RELEASE_DIR := _dist

# Image URL to use all building/pushing image targets
IMG ?= ${REGISTRY}/${IMAGE_NAME}:${TAG}
BYOH_BASE_IMG = byoh/node:e2e
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)

REPO_ROOT := $(shell pwd)
# Go version of the container images that build the binaries, taken from the
# toolchain directive in go.mod.
GO_VERSION ?= $(shell sed -n 's/^toolchain go//p' go.mod)
GINKGO_FOCUS  ?=
GINKGO_SKIP ?=
GINKGO_NODES  ?= 1
E2E_CONF_FILE  ?= ${REPO_ROOT}/test/e2e/config/provider.yaml
ARTIFACTS ?= ${REPO_ROOT}/_artifacts
SKIP_RESOURCE_CLEANUP ?= false
USE_EXISTING_CLUSTER ?= false
EXISTING_CLUSTER_KUBECONFIG_PATH ?=
GINKGO_NOCOLOR ?= false

TOOLS_DIR := $(REPO_ROOT)/hack/tools
BIN_DIR := bin
TOOLS_BIN_DIR := $(TOOLS_DIR)/$(BIN_DIR)
GINKGO := $(TOOLS_BIN_DIR)/ginkgo
GINKGO_PKG := github.com/onsi/ginkgo/v2/ginkgo
SETUP_ENVTEST_VER := v0.25.2
SETUP_ENVTEST := $(TOOLS_BIN_DIR)/setup-envtest
# Kubernetes version of the control plane binaries that envtest runs.
ENVTEST_K8S_VERSION ?= 1.36.2
ENVTEST_ASSETS = KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use -p path --bin-dir $(TOOLS_BIN_DIR) $(ENVTEST_K8S_VERSION))"

BYOH_TEMPLATES := $(REPO_ROOT)/test/e2e/data/infrastructure-provider-byoh

LDFLAGS := -w -s $(shell hack/version.sh)
STATIC=-extldflags '-static'

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.DEFAULT_GOAL := help

all: build

HOST_AGENT_DIR ?= agent
# The architectures to build the manager image and the host agent for.
ALL_ARCH ?= amd64 arm64
HOST_AGENT_ARCHS ?= $(ALL_ARCH)

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# https://linuxcommand.org/lc3_adv_awk.php

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

manifests: controller-gen yq ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) crd:crdVersions=v1 rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_bootstrapkubeconfigs.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byoclusters.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byoclustertemplates.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byohosts.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byomachines.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_byomachinetemplates.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_k8sinstallerconfigs.yaml
	$(YQ) -i eval 'del(.metadata.creationTimestamp)' config/crd/bases/infrastructure.cluster.x-k8s.io_k8sinstallerconfigtemplates.yaml


generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

GOLANGCI_LINT_VER := v2.12.2
GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint-$(GOLANGCI_LINT_VER)
lint: golangci-lint ## Run golangci-lint.
	$(GOLANGCI_LINT) config verify
	$(GOLANGCI_LINT) run
golangci-lint: ## Download golangci-lint locally if necessary.
	$(call go-get-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VER))

update-containerd-service: ## Update the systemd unit of containerd in the installer to the one of its default version.
	hack/update-containerd-service.sh

# CI runs this target, so that the default version of containerd does not
# change without its systemd unit.
verify-containerd-service: update-containerd-service ## Verify that the systemd unit of containerd in the installer is the one of its default version.
	git diff --exit-code -- installer/internal/algo/containerd.service

##@ Build

build: generate fmt vet ## Build manager binary.
	go build -o bin/manager main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

docker-build: ## Build docker image with the manager for the platform of this machine.
	docker build --build-arg GO_VERSION=$(GO_VERSION) -t ${IMG} .

docker-push: ## Push docker image with the manager.
	docker push ${IMG}

empty :=
space := $(empty) $(empty)
comma := ,
# The platforms of the multi-platform image, linux/<arch> for each of ALL_ARCH.
IMAGE_PLATFORMS ?= $(subst $(space),$(comma),$(addprefix linux/,$(ALL_ARCH)))
# Set to true to push the multi-platform image to the registry of IMG.
DOCKER_BUILDX_PUSH ?= false

docker-buildx: ## Build docker image with the manager for all the platforms in ALL_ARCH, and push it if DOCKER_BUILDX_PUSH is true.
	docker buildx build --platform $(IMAGE_PLATFORMS) --build-arg GO_VERSION=$(GO_VERSION) -t ${IMG} $(if $(filter true,$(DOCKER_BUILDX_PUSH)),--push) .

prepare-byoh-docker-host-image:
	docker build test/e2e -f test/e2e/BYOHDockerFile -t ${BYOH_BASE_IMG}

cluster-templates-v1beta1: kustomize ## Generate cluster templates for v1beta1
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/vm --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template.yaml
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/docker --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template.yaml

##@ Test

# Run tests
test: $(GINKGO) generate fmt vet manifests test-coverage ## Run unit tests

test-coverage: prepare-byoh-docker-host-image $(GINKGO) $(SETUP_ENVTEST) ## Run test-coverage
	$(ENVTEST_ASSETS) $(GINKGO) --randomize-all -r --cover --coverprofile=cover.out --output-dir=. --skip-package=test .

agent-test: prepare-byoh-docker-host-image $(GINKGO) $(SETUP_ENVTEST) ## Run agent tests
	$(ENVTEST_ASSETS) $(GINKGO) --randomize-all -r --coverprofile cover.out $(HOST_AGENT_DIR)

controller-test: $(GINKGO) $(SETUP_ENVTEST) ## Run controller tests
	$(ENVTEST_ASSETS) $(GINKGO) --randomize-all --coverprofile cover.out --vv controllers/infrastructure

webhook-test: $(GINKGO) $(SETUP_ENVTEST) ## Run webhook tests
	$(ENVTEST_ASSETS) $(GINKGO) --coverprofile cover.out apis/infrastructure/v1beta1

_SKIP_ARGS := $(if $(GINKGO_SKIP),-skip="$(GINKGO_SKIP)")

# The test framework of Cluster API runs kustomize for the provider from
# config/default.
test-e2e: take-user-input docker-build prepare-byoh-docker-host-image $(GINKGO) cluster-templates-e2e ## Run the end-to-end tests
	CAPI_KUSTOMIZE_PATH=$(KUSTOMIZE) $(GINKGO) -v -trace -tags=e2e -focus="$(GINKGO_FOCUS)" $(_SKIP_ARGS) -nodes=$(GINKGO_NODES) --no-color=$(GINKGO_NOCOLOR) $(GINKGO_ARGS) test/e2e -- \
	    -e2e.artifacts-folder="$(ARTIFACTS)" \
	    -e2e.config="$(E2E_CONF_FILE)" \
	    -e2e.skip-resource-cleanup=$(SKIP_RESOURCE_CLEANUP) -e2e.use-existing-cluster=$(USE_EXISTING_CLUSTER) \
		-e2e.existing-cluster-kubeconfig-path=$(EXISTING_CLUSTER_KUBECONFIG_PATH)

# The pull requests run only this spec, which is the one to run locally first.
test-e2e-pr-blocking: GINKGO_FOCUS := \[PR-Blocking\]
test-e2e-pr-blocking: test-e2e ## Run the [PR-Blocking] end-to-end test, which the pull requests run

cluster-templates: kustomize cluster-templates-v1beta1

cluster-templates-e2e: kustomize
	$(KUSTOMIZE) build $(BYOH_TEMPLATES)/v1beta1/templates/e2e --load-restrictor LoadRestrictionsNone > $(BYOH_TEMPLATES)/v1beta1/templates/e2e/cluster-template.yaml

define WARNING
#####################################################################################################

** WARNING **
These tests modify system settings - and do **NOT** revert them at the end of the test.
A list of changes can be found below. We **highly** recommend running these tests in a VM. 

Running e2e tests locally will change the following host config
- enable the kernel modules: overlay & bridge network filter
- create a systemwide config that will enable those modules at boot time
- enable ipv4 & ipv6 forwarding
- create a systemwide config that will enable the forwarding at boot time
- reload the sysctl with the applied config changes so the changes can take effect without restarting
- disable unattended OS updates

#####################################################################################################
endef
export WARNING

.PHONY: take-user-input
take-user-input:
	@echo "$$WARNING"
	@read -p "Do you want to proceed [Y/n]?" REPLY; \
	if [[ $$REPLY = "Y" || $$REPLY = "y" ]]; then echo starting e2e test; exit 0 ; else echo aborting; exit 1; fi
	


$(GINKGO): # Build ginkgo from tools folder.
	cd $(TOOLS_DIR); GOBIN=$(TOOLS_BIN_DIR) go install $(GINKGO_PKG)

$(SETUP_ENVTEST): # Build setup-envtest, which downloads the envtest binaries.
	GOBIN=$(TOOLS_BIN_DIR) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VER)

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image ghcr.io/superbrothers/cluster-api-byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

publish-infra-yaml:kustomize # Generate infrastructure-components.yaml for the provider
	cd config/manager && $(KUSTOMIZE) edit set image ghcr.io/superbrothers/cluster-api-byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default > infrastructure-components.yaml

CONTROLLER_GEN_VER := v0.21.0
CONTROLLER_GEN = $(shell pwd)/bin/controller-gen-$(CONTROLLER_GEN_VER)
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VER))

KUSTOMIZE_VER := v5.7.0
KUSTOMIZE = $(shell pwd)/bin/kustomize-$(KUSTOMIZE_VER)
kustomize: ## Download kustomize locally if necessary.
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VER))

YQ_VER := v4.45.4
YQ = $(shell pwd)/bin/yq-$(YQ_VER)
yq: ## Download yq locally if necessary.
	$(call go-get-tool,$(YQ),github.com/mikefarah/yq/v4@$(YQ_VER))

NFPM_VER := v2.48.0
# Not in bin, which the container that builds the host agent creates as root.
NFPM = $(TOOLS_BIN_DIR)/nfpm-$(NFPM_VER)
nfpm: ## Download nfpm locally if necessary.
	$(call go-get-tool,$(NFPM),github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VER))

host-agent-binaries: $(addprefix host-agent-binary-,$(HOST_AGENT_ARCHS)) ## Builds the binaries for the host-agent

host-agent-binary-%:
	RELEASE_BINARY=./byoh-hostagent GOOS=linux GOARCH=$* GOLDFLAGS="$(LDFLAGS) $(STATIC)" \
	HOST_AGENT_DIR=./$(HOST_AGENT_DIR) $(MAKE) host-agent-binary

host-agent-binary: $(RELEASE_DIR)
	docker run \
		--rm \
		-e CGO_ENABLED=0 \
		-e GOOS=$(GOOS) \
		-e GOARCH=$(GOARCH) \
		-v "$$(pwd):/workspace$(DOCKER_VOL_OPTS)" \
		-w /workspace \
		golang:$(GO_VERSION) \
		go build -buildvcs=false -a -ldflags "$(GOLDFLAGS)" \
		-o ./bin/$(notdir $(RELEASE_BINARY))-$(GOOS)-$(GOARCH) $(HOST_AGENT_DIR)

# The version of the deb packages of the host agent in semver, without the
# leading v: the tag on HEAD, the last tag followed by the number of commits
# since it and the commit, or, when there is no tag, 0.0.0-g followed by the
# commit, with .dirty when the tree has changes, for the agent that reports
# v0.0.0-<commit>. The commit has the prefix g, as a pre-release identifier of
# only digits must not start with 0. It names the files as it is, and nfpm turns
# a pre-release such as 0.6.0-rc.0 into the version 0.6.0~rc.0 in the package,
# which dpkg orders before 0.6.0.
DEB_COMMIT := $(shell git rev-parse --short=14 HEAD 2>/dev/null)
DEB_DEV_VERSION = 0.0.0-$(if $(DEB_COMMIT),g$(DEB_COMMIT)$(if $(shell git status --porcelain 2>/dev/null),.dirty),unknown)
DEB_VERSION ?= $(or $(shell git describe --tags --match 'v[0-9]*' 2>/dev/null | sed -E 's/^v//; s/-([0-9]+)-g([0-9a-f]+)$$/+\1.g\2/'),$(DEB_DEV_VERSION))

host-agent-debs: $(addprefix host-agent-deb-,$(HOST_AGENT_ARCHS)) ## Build the deb packages of the host agent

host-agent-deb-%: host-agent-binary-% nfpm $(RELEASE_DIR)
	GOARCH=$* DEB_VERSION=$(DEB_VERSION) $(NFPM) package \
		--config packaging/byoh-hostagent/nfpm.yaml \
		--packager deb \
		--target $(RELEASE_DIR)/byoh-hostagent_$(DEB_VERSION)_$*.deb

##@ Release

$(RELEASE_DIR):
	mkdir -p $(RELEASE_DIR)

build-release-artifacts: build-cluster-templates build-infra-yaml build-metadata-yaml build-host-agent-binary host-agent-debs ## Builds release artifacts

build-cluster-templates: $(RELEASE_DIR) cluster-templates
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template.yaml $(RELEASE_DIR)/cluster-template-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/cluster-template-topology-docker.yaml $(RELEASE_DIR)/cluster-template-topology-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/docker/clusterclass-quickstart-docker.yaml $(RELEASE_DIR)/clusterclass-quickstart-docker.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template.yaml $(RELEASE_DIR)/cluster-template.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/cluster-template-topology.yaml $(RELEASE_DIR)/cluster-template-topology.yaml
	cp $(BYOH_TEMPLATES)/v1beta1/templates/vm/clusterclass-quickstart.yaml $(RELEASE_DIR)/clusterclass-quickstart.yaml


build-infra-yaml:kustomize ## Generate infrastructure-components.yaml for the provider
	cd config/manager && $(KUSTOMIZE) edit set image ghcr.io/superbrothers/cluster-api-byoh-controller=${IMG}
	$(KUSTOMIZE) build config/default > $(RELEASE_DIR)/infrastructure-components.yaml

build-metadata-yaml:
	cp metadata.yaml $(RELEASE_DIR)/metadata.yaml

build-host-agent-binary: host-agent-binaries
	cp $(addprefix bin/byoh-hostagent-linux-,$(HOST_AGENT_ARCHS)) $(RELEASE_DIR)/


# go-get-tool installs the package $2 (with @version) as the binary $1. The
# binary names carry the version, so that changing a version installs it again.
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
echo "Downloading $(2)" ;\
TMP_DIR=$$(mktemp -d) ;\
GOBIN=$$TMP_DIR go install $(2) ;\
mkdir -p $(dir $(1)) ;\
mv $$TMP_DIR/* $(1) ;\
rm -rf $$TMP_DIR ;\
}
endef
