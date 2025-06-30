# Quay registry configuration - primary image naming system
QUAY_REGISTRY ?= quay.io
QUAY_ORG ?= medik8s
OPERATOR_IMG ?= sbd-operator
AGENT_IMG ?= sbd-agent
QUAY_OPERATOR_IMG ?= $(QUAY_REGISTRY)/$(QUAY_ORG)/$(OPERATOR_IMG)
QUAY_AGENT_IMG ?= $(QUAY_REGISTRY)/$(QUAY_ORG)/$(AGENT_IMG)

# VERSION defines the project version for the bundle.
# Update this value when you upgrade the version of your project.
# To re-generate a bundle for another specific version without changing the standard setup, you can:
# - use the VERSION as arg of the bundle target (e.g make bundle VERSION=0.0.2)
# - use environment variables to overwrite this value (e.g export VERSION=0.0.2)
VERSION ?= latest
TAG ?= latest

# Build information
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_DESCRIBE ?= $(shell git describe --tags --dirty 2>/dev/null || echo "unknown")

# Legacy IMG variable for backwards compatibility (maps to operator image)
IMG ?= $(QUAY_OPERATOR_IMG):$(TAG)
OPERATOR_SHA=$$(podman inspect $(QUAY_OPERATOR_IMG):$(TAG) --format "{{.ID}}" )
AGENT_SHA=$$(podman inspect $(QUAY_AGENT_IMG):$(TAG) --format "{{.ID}}" )

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
CONTAINER_TOOL ?= podman

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build build-agent

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
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test-all
test-all: test test-smoke test-e2e ## Run all tests: unit tests, smoke tests, and e2e tests

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v -E '/(e2e|smoke)') -coverprofile cover.out

.PHONY: test-e2e
test-e2e: ## Run e2e tests using the test runner script (auto-detects environment).
	@echo "Running e2e tests using test runner script..."
	@scripts/run-tests.sh --type e2e -v --env cluster

.PHONY: test-smoke
test-smoke: ## Run smoke tests with building images.
	@echo "Running smoke tests with image building..."
	@scripts/run-tests.sh --type smoke --env cluster -v

.PHONY: load-images
load-images:
	@echo "Loading images into CRC..."
	$(CONTAINER_TOOL) save --format docker-archive $(QUAY_OPERATOR_IMG):$(TAG) -o bin/$(OPERATOR_IMG).tar
	$(CONTAINER_TOOL) save --format docker-archive $(QUAY_AGENT_IMG):$(TAG) -o bin/$(AGENT_IMG).tar
	@eval $$(crc podman-env) && $(CONTAINER_TOOL) load -i bin/$(OPERATOR_IMG).tar
	@eval $$(crc podman-env) && $(CONTAINER_TOOL) load -i bin/$(AGENT_IMG).tar

.PHONY: test-smoke-reload
test-smoke-reload:
	@echo "Reloading operator deployment..."
	@eval $$(crc oc-env) && kubectl patch deployment sbd-operator-controller-manager -n sbd-operator-system -p '{"spec":{"template":{"spec":{"containers":[{"name":"manager","image":"$(QUAY_OPERATOR_IMG)@sha256:$$(podman inspect $(QUAY_AGENT_IMG):$(TAG) --format "{{.ID}}"| head -c 12 )","imagePullPolicy":"Never"}]}}}}'
	@eval $$(crc oc-env) && kubectl patch sbdconfig test-config -n sbd-operator-system -p '{"spec":{"image":"$(QUAY_AGENT_IMG)@sha256:$$(podman inspect $(QUAY_AGENT_IMG):$(TAG) --format "{{.ID}}"| head -c 12 )"}}'
	#	OPERATOR_IMG="$(QUAY_OPERATOR_IMG)@sha256:$(OPERATOR_SHA)" \
	#	AGENT_IMG="$(QUAY_AGENT_IMG)@sha256:$(AGENT_SHA)" \

##@ OpenShift on AWS

# AWS OpenShift Cluster Configuration
# The provisioning script automatically downloads and installs required tools:
# - AWS CLI v2, openshift-install, oc CLI, and jq
# Prerequisites: AWS credentials configured, Red Hat pull secret
OCP_CLUSTER_NAME ?= beekhof-sbd-operator-test
AWS_REGION ?= us-east-1
OCP_WORKER_COUNT ?= 4
OCP_INSTANCE_TYPE ?= m5.large
OCP_VERSION ?= 4.18.18
OCP_BASE_DOMAIN ?= aws.validatedpatterns.io

.PHONY: provision-ocp-aws
provision-ocp-aws: ## Provision OpenShift cluster on AWS (auto-installs required tools)
	@echo "Provisioning OpenShift cluster on AWS with automatic tool installation..."
	@chmod +x scripts/provision-ocp-aws.sh
	@scripts/provision-ocp-aws.sh \
		--cluster-name $(OCP_CLUSTER_NAME) \
		--region $(AWS_REGION) \
		--workers $(OCP_WORKER_COUNT) \
		--instance-type $(OCP_INSTANCE_TYPE) \
		--ocp-version $(OCP_VERSION) \
		--base-domain $(OCP_BASE_DOMAIN)

.PHONY: destroy-ocp-aws
destroy-ocp-aws: ## Destroy OpenShift cluster on AWS
	@echo "Destroying OpenShift cluster $(OCP_CLUSTER_NAME) on AWS..."
	@if [ -d "cluster" ]; then \
		cd cluster && openshift-install destroy cluster --log-level=info; \
		cd .. && rm -rf cluster; \
	else \
		echo "No cluster directory found - cluster may already be destroyed"; \
	fi

.PHONY: provision-and-test-e2e
provision-and-test-e2e: ## Provision AWS cluster and run e2e tests
	@echo "Provisioning AWS cluster and running e2e tests..."
	@$(MAKE) provision-ocp-aws
	@echo "Waiting for cluster to be ready..."
	@sleep 60
	@echo "Setting up kubeconfig..."
	@export KUBECONFIG=$$(pwd)/cluster/auth/kubeconfig
	@$(MAKE) test-e2e
	@if [ "$(CLEANUP_AFTER_TEST)" = "true" ]; then \
		echo "Cleaning up AWS cluster..."; \
		$(MAKE) destroy-ocp-aws; \
	else \
		echo "Cluster preserved. Run 'make destroy-ocp-aws' to clean up manually."; \
	fi



destroy-crc:
	@echo "Deleting CRC cluster..."
	@crc delete -f || true

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
build: manifests generate fmt vet ## Build manager binary.
	go build -ldflags="-X 'github.com/medik8s/sbd-operator/pkg/version.GitCommit=$(GIT_COMMIT)' \
		-X 'github.com/medik8s/sbd-operator/pkg/version.GitDescribe=$(GIT_DESCRIBE)' \
		-X 'github.com/medik8s/sbd-operator/pkg/version.BuildDate=$(BUILD_DATE)'" \
		-o bin/manager cmd/main.go

.PHONY: build-agent
build-agent: manifests generate fmt vet ## Build SBD agent binary.
	go build -ldflags="-X 'github.com/medik8s/sbd-operator/pkg/version.GitCommit=$(GIT_COMMIT)' \
		-X 'github.com/medik8s/sbd-operator/pkg/version.GitDescribe=$(GIT_DESCRIBE)' \
		-X 'github.com/medik8s/sbd-operator/pkg/version.BuildDate=$(BUILD_DATE)'" \
		-o bin/sbd-agent cmd/sbd-agent/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

##@ Container Images

# Primary build targets (Quay-first approach)
# Use these for standard development and CI/CD workflows
# Example: make build-images VERSION=v1.0.0
# Example: make build-push QUAY_REGISTRY=my-registry.io QUAY_ORG=myorg

# PLATFORMS defines the target platforms for multi-platform builds
PLATFORMS ?= linux/arm64,linux/amd64 # Others: linux/s390x,linux/ppc64le

.PHONY: build-operator-image
build-operator-image: manifests generate fmt vet ## Build operator container image.
	@echo "Building operator image: $(QUAY_OPERATOR_IMG):$(TAG)"
	@echo "Build info: GitDescribe=$(GIT_DESCRIBE), GitCommit=$(GIT_COMMIT), BuildDate=$(BUILD_DATE)"
	$(CONTAINER_TOOL) build -t sbd-operator:$(TAG) \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg GIT_COMMIT="$(GIT_COMMIT)" \
		--build-arg GIT_DESCRIBE="$(GIT_DESCRIBE)" \
		.
	$(CONTAINER_TOOL) tag sbd-operator:$(TAG) $(QUAY_OPERATOR_IMG):$(TAG)

.PHONY: build-agent-image  
build-agent-image: manifests generate fmt vet ## Build agent container image.
	@echo "Building agent image: $(QUAY_AGENT_IMG):$(TAG)"
	@echo "Build info: GitDescribe=$(GIT_DESCRIBE), GitCommit=$(GIT_COMMIT), BuildDate=$(BUILD_DATE)"
	$(CONTAINER_TOOL) build -f Dockerfile.sbd-agent -t sbd-agent:$(TAG) \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg GIT_COMMIT="$(GIT_COMMIT)" \
		--build-arg GIT_DESCRIBE="$(GIT_DESCRIBE)" \
		.
	$(CONTAINER_TOOL) tag sbd-agent:$(TAG) $(QUAY_AGENT_IMG):$(TAG)

.PHONY: build-multiarch-operator-image
build-multiarch-operator-image: manifests generate fmt vet ## Build multi-platform operator container image.
	@echo "Building multi-platform operator image: $(QUAY_OPERATOR_IMG):$(TAG)"
	@echo "Platforms: $(PLATFORMS)"
	@echo "Build info: GitDescribe=$(GIT_DESCRIBE), GitCommit=$(GIT_COMMIT), BuildDate=$(BUILD_DATE)"
	$(CONTAINER_TOOL) build --platform=$(PLATFORMS) -t $(QUAY_OPERATOR_IMG):$(TAG) \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg GIT_COMMIT="$(GIT_COMMIT)" \
		--build-arg GIT_DESCRIBE="$(GIT_DESCRIBE)" \
		.

.PHONY: build-multiarch-agent-image
build-multiarch-agent-image: manifests generate fmt vet ## Build multi-platform agent container image.
	@echo "Building multi-platform agent image: $(QUAY_AGENT_IMG):$(TAG)"
	@echo "Platforms: $(PLATFORMS)"
	@echo "Build info: GitDescribe=$(GIT_DESCRIBE), GitCommit=$(GIT_COMMIT), BuildDate=$(BUILD_DATE)"
	$(CONTAINER_TOOL) build --platform=$(PLATFORMS) -f Dockerfile.sbd-agent -t $(QUAY_AGENT_IMG):$(TAG) \
		--build-arg BUILD_DATE="$(BUILD_DATE)" \
		--build-arg GIT_COMMIT="$(GIT_COMMIT)" \
		--build-arg GIT_DESCRIBE="$(GIT_DESCRIBE)" \
		.

.PHONY: build-images
build-images: build-operator-image build-agent-image ## Build both operator and agent container images.
	@echo "Built SBD Operator images..."
	@echo "Operator: $(QUAY_OPERATOR_IMG):$(TAG)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(TAG)"

.PHONY: build-multiarch-images
build-multiarch-images: build-multiarch-operator-image build-multiarch-agent-image ## Build both operator and agent multi-platform container images.
	@echo "Built multi-platform SBD Operator images..."
	@echo "Operator: $(QUAY_OPERATOR_IMG):$(TAG)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(TAG)"
	@echo "Platforms: $(PLATFORMS)"

.PHONY: push-operator-image
push-operator-image: ## Push operator container image to registry.
	@echo "Pushing operator image: $(QUAY_OPERATOR_IMG):$(TAG)"
	$(CONTAINER_TOOL) push $(QUAY_OPERATOR_IMG):$(TAG)

.PHONY: push-agent-image
push-agent-image: ## Push agent container image to registry.
	@echo "Pushing agent image: $(QUAY_AGENT_IMG):$(TAG)"
	$(CONTAINER_TOOL) push $(QUAY_AGENT_IMG):$(TAG)

.PHONY: push-images
push-images: push-operator-image push-agent-image ## Push both operator and agent container images to registry.
	@echo "Pushed SBD images to registry..."

.PHONY: push-multiarch-operator-image
push-multiarch-operator-image: ## Push multi-platform operator container image to registry.
	@echo "Pushing multi-platform operator image: $(QUAY_OPERATOR_IMG):$(TAG)"
	$(CONTAINER_TOOL) manifest push $(QUAY_OPERATOR_IMG):$(TAG)

.PHONY: push-multiarch-agent-image
push-multiarch-agent-image: ## Push multi-platform agent container image to registry.
	@echo "Pushing multi-platform agent image: $(QUAY_AGENT_IMG):$(TAG)"
	$(CONTAINER_TOOL) manifest push $(QUAY_AGENT_IMG):$(TAG)

.PHONY: push-multiarch-images
push-multiarch-images: push-multiarch-operator-image push-multiarch-agent-image ## Push both operator and agent multi-platform container images to registry.
	@echo "Pushed multi-platform SBD images to registry..."

.PHONY: build-push
build-push: update-manifests build-images push-images ## Build and push both operator and agent images to registry.

.PHONY: build-push-multiarch
build-push-multiarch: update-manifests build-multiarch-images push-multiarch-images ## Build and push both operator and agent multi-platform images to registry.

.PHONY: buildx
buildx: build-push-multiarch ## Build and push multi-platform images to registry (alias for build-push-multiarch).
	@echo "✅ Successfully built and pushed multi-platform images!"

##@ Legacy Docker Aliases (Deprecated - Use build-* targets instead)

.PHONY: docker-build
docker-build: build-images ## Legacy alias with IMG support (deprecated - use build-images instead).
	@echo "⚠️  Warning: 'docker-build' is deprecated. Use 'make build-images' instead."

.PHONY: docker-push
docker-push: push-images ## Legacy alias for push-images (deprecated).
	@echo "⚠️  Warning: 'docker-push' is deprecated. Use 'make push-images' instead."

.PHONY: docker-buildx
docker-buildx: buildx ## Legacy alias for buildx (deprecated).
	@echo "⚠️  Warning: 'docker-buildx' is deprecated. Use 'make buildx' instead."


.PHONY: update-manifests
update-manifests: ## Update all manifests to use current QUAY image references (auto-runs with build-push).
	@echo "Updating manifests with image references..."
	@echo "Operator: $(QUAY_OPERATOR_IMG):$(TAG) aka. $(OPERATOR_SHA)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(TAG)  aka. $(AGENT_SHA)"
	
	# Update agent daemonset manifests
	@for file in deploy/sbd-agent-daemonset*.yaml; do \
		if [ -f "$$file" ]; then \
			echo "Updating $$file..."; \
			sed -i.bak 's|image: quay\.io/medik8s/sbd-agent:.*|image: $(QUAY_AGENT_IMG):$(TAG)|g' "$$file"; \
			rm -f "$$file.bak"; \
		fi; \
	done
	
	# Update sample configs
	@for file in config/samples/*.yaml; do \
		if [ -f "$$file" ] && grep -q 'image:' "$$file"; then \
			echo "Updating $$file..."; \
			sed -i.bak 's|image: "quay\.io/medik8s/sbd-agent:.*"|image: "$(QUAY_AGENT_IMG):$(TAG)"|g' "$$file"; \
			rm -f "$$file.bak"; \
		fi; \
	done
	
	@echo "Manifests updated successfully!"

.PHONY: build-installer
build-installer: update-manifests manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(QUAY_OPERATOR_IMG):$(TAG)
	$(KUSTOMIZE) build config/default > dist/install.yaml

.PHONY: build-openshift-installer
build-openshift-installer: update-manifests manifests generate kustomize ## Generate a consolidated YAML with CRDs, deployment, and OpenShift SecurityContextConstraints.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(QUAY_OPERATOR_IMG):$(TAG)
	$(KUSTOMIZE) build config/openshift-default > dist/install.yaml



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
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
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

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= v2.1.0

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

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

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
