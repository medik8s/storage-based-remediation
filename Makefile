# Quay registry configuration - primary image naming system
QUAY_REGISTRY ?= quay.io
QUAY_ORG ?= medik8s
OPERATOR_IMG ?= sbd-operator
AGENT_IMG ?= sbd-agent
QUAY_OPERATOR_IMG ?= $(QUAY_REGISTRY)/$(QUAY_ORG)/$(OPERATOR_IMG)
QUAY_AGENT_IMG ?= $(QUAY_REGISTRY)/$(QUAY_ORG)/$(AGENT_IMG)
VERSION ?= latest

# Legacy IMG variable for backwards compatibility (maps to operator image)
IMG ?= $(QUAY_OPERATOR_IMG):$(VERSION)

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

.PHONY: test
test: manifests generate fmt vet setup-envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: test-all
test-all: test test-smoke test-multinode ## Run all tests: unit tests, smoke tests, and multinode tests

# Smoke Test Configuration
# The smoke tests can either reuse an existing CRC environment or recreate it from scratch.
# Use 'test-smoke' to reuse existing CRC (faster) or 'test-smoke-fresh' for clean environment.
#
# Environment Variables:
# - SMOKE_CLEANUP_SKIP=true: Skip cleanup after tests (useful for debugging)
# - CERT_MANAGER_INSTALL_SKIP=true: Skip CertManager installation
# - QUAY_REGISTRY: Container registry (default: quay.io)
# - QUAY_ORG: Container organization (default: medik8s)
# - VERSION: Image version tag (default: latest)
#
# The setup-test-smoke target handles:
# 1. Starting CRC cluster (only if not already running)
# 2. Building and loading container images
# 3. Installing CRDs
# 4. Deploying the operator
# 5. Waiting for operator readiness
CRC_CLUSTER ?= sbd-operator-test-smoke

.PHONY: load-images
load-images:
	@echo "Loading images into CRC..."
	$(CONTAINER_TOOL) save --format docker-archive $(QUAY_OPERATOR_IMG):$(VERSION) -o bin/$(OPERATOR_IMG).tar
	$(CONTAINER_TOOL) save --format docker-archive $(QUAY_AGENT_IMG):$(VERSION) -o bin/$(AGENT_IMG).tar
	@eval $$(crc podman-env) && $(CONTAINER_TOOL) load -i bin/$(OPERATOR_IMG).tar
	@eval $$(crc podman-env) && $(CONTAINER_TOOL) load -i bin/$(AGENT_IMG).tar

.PHONY: setup-test-smoke
setup-test-smoke: ## Set up CRC environment for smoke tests (start CRC only if not running)
	@command -v crc >/dev/null 2>&1 || { \
		echo "CRC is not installed. Please install CRC manually."; \
		echo "Visit: https://developers.redhat.com/products/codeready-containers/download"; \
		exit 1; \
	}
	@echo "Setting up CRC environment for smoke tests..."
	@if crc status | grep -q "CRC VM.*Running"; then \
		echo "CRC is already running, skipping CRC start..."; \
	else \
		echo "CRC is not running, starting CRC cluster..."; \
		crc start; \
	fi
	@echo "Setting up CRC environment..."
	@eval $$(crc oc-env) && oc whoami || { \
		echo "Failed to authenticate with CRC cluster"; \
		exit 1; \
	}
	@echo "Building and loading container images..."
	#TODO:$@(MAKE) build-images
	@$(MAKE) load-images
	@echo "Building OpenShift installer with SecurityContextConstraints..."
	@$(MAKE) build-openshift-installer
	@echo "Deploying operator to CRC with OpenShift support..."
	@eval $$(crc oc-env) && kubectl apply -f dist/install-openshift.yaml --server-side=true
	@echo "Waiting for operator to be ready..."
	@eval $$(crc oc-env) && kubectl wait --for=condition=ready pod -l control-plane=controller-manager -n sbd-operator-system --timeout=120s || { \
		echo "Operator failed to start, checking logs..."; \
		kubectl logs -n sbd-operator-system -l control-plane=controller-manager --tail=20 || true; \
		exit 1; \
	}
	@echo "Smoke test environment setup complete!"

.PHONY: test-smoke-fresh
test-smoke-fresh: destroy-crc setup-test-smoke test-smoke

.PHONY: test-smoke
test-smoke: setup-test-smoke ## Run the smoke tests on CRC OpenShift cluster (setup handled in setup-test-smoke).
	@echo "Running smoke tests on CRC OpenShift cluster..."
	@eval $$(crc oc-env) && \
	QUAY_REGISTRY=$(QUAY_REGISTRY) QUAY_ORG=$(QUAY_ORG) VERSION=$(VERSION) \
	go test ./test/e2e/ -v -ginkgo.v -ginkgo.focus="Smoke"; \
	TEST_EXIT_CODE=$$?; \
	if [ "$(SMOKE_CLEANUP_SKIP)" != "true" ]; then \
		echo "Cleaning up smoke test environment (set SMOKE_CLEANUP_SKIP=true to skip)..."; \
		$(MAKE) cleanup-test-smoke; \
	else \
		echo "Skipping cleanup (SMOKE_CLEANUP_SKIP=true)"; \
	fi; \
	exit $$TEST_EXIT_CODE

.PHONY: test-smoke-crc
test-smoke-crc: ## Run smoke tests specifically on CRC OpenShift cluster
	@echo "Setting up and running smoke tests on CRC OpenShift cluster..."
	@export USE_CRC=true && $(MAKE) test-smoke

.PHONY: test-smoke-kind
test-smoke-kind: ## Run smoke tests on Kind Kubernetes cluster (legacy support)
	@echo "Setting up and running smoke tests on Kind cluster..."
	@export USE_CRC=false && \
	command -v kind >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	} && \
	if ! kind get clusters | grep -q "$(CRC_CLUSTER)"; then \
		kind create cluster --name $(CRC_CLUSTER); \
	fi && \
	KIND_CLUSTER=$(CRC_CLUSTER) \
	QUAY_REGISTRY=$(QUAY_REGISTRY) QUAY_ORG=$(QUAY_ORG) VERSION=$(VERSION) \
	go test ./test/e2e/ -v -ginkgo.v -ginkgo.focus="Smoke" && \
	kind delete cluster --name $(CRC_CLUSTER)

##@ OpenShift on AWS

# AWS OpenShift Cluster Configuration
# The provisioning script automatically downloads and installs required tools:
# - AWS CLI v2, openshift-install, oc CLI, and jq
# Prerequisites: AWS credentials configured, Red Hat pull secret
OCP_CLUSTER_NAME ?= sbd-operator-test
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
		--base-domain $(OCP_BASE_DOMAIN) \
		--cleanup

.PHONY: destroy-ocp-aws
destroy-ocp-aws: ## Destroy OpenShift cluster on AWS
	@echo "Destroying OpenShift cluster $(OCP_CLUSTER_NAME) on AWS..."
	@if [ -d "cluster" ]; then \
		cd cluster && openshift-install destroy cluster --log-level=info; \
		cd .. && rm -rf cluster; \
	else \
		echo "No cluster directory found - cluster may already be destroyed"; \
	fi

.PHONY: test-multinode
test-multinode: ## Run multi-node tests on existing OpenShift cluster
	@echo "Running multi-node tests on existing OpenShift cluster..."
	@command -v kubectl >/dev/null 2>&1 || { \
		echo "kubectl is not installed. Please install it manually."; \
		exit 1; \
	}
	@echo "Checking cluster connectivity..."
	@kubectl cluster-info || { \
		echo "Cannot connect to Kubernetes cluster. Please ensure KUBECONFIG is set correctly."; \
		exit 1; \
	}
	@echo "Running multi-node test suite..."
	@go test ./test/e2e/ -v -ginkgo.v -ginkgo.focus="Multi-Node"

.PHONY: provision-and-test-multinode
provision-and-test-multinode: ## Provision AWS cluster and run multi-node tests
	@echo "Provisioning AWS cluster and running multi-node tests..."
	@$(MAKE) provision-ocp-aws
	@echo "Waiting for cluster to be ready..."
	@sleep 60
	@echo "Setting up kubeconfig..."
	@export KUBECONFIG=$$(pwd)/cluster/auth/kubeconfig
	@$(MAKE) test-multinode
	@if [ "$(CLEANUP_AFTER_TEST)" = "true" ]; then \
		echo "Cleaning up AWS cluster..."; \
		$(MAKE) destroy-ocp-aws; \
	else \
		echo "Cluster preserved. Run 'make destroy-ocp-aws' to clean up manually."; \
	fi

.PHONY: cleanup-test-smoke
cleanup-test-smoke: ## Clean up smoke test environment and stop CRC cluster
	@echo "Cleaning up smoke test environment..."
	@eval $$(crc oc-env) && kubectl delete ns sbd-operator-system --ignore-not-found=true || true
	@eval $$(crc oc-env) && kubectl delete ns sbd-system --ignore-not-found=true || true
	@eval $$(crc oc-env) && kubectl delete sbdconfig --all --ignore-not-found=true || true
	@eval $$(crc oc-env) && kubectl delete clusterrolebinding -l app.kubernetes.io/managed-by=sbd-operator --ignore-not-found=true || true
	@eval $$(crc oc-env) && kubectl delete clusterrole -l app.kubernetes.io/managed-by=sbd-operator --ignore-not-found=true || true
	@echo "Cleaning up OpenShift-specific resources..."
	@eval $$(crc oc-env) && kubectl delete scc sbd-operator-sbd-agent-privileged --ignore-not-found=true || true
	@eval $$(crc oc-env) && kubectl delete clusterrolebinding sbd-operator-sbd-agent-scc-user --ignore-not-found=true || true
	@eval $$(crc oc-env) && kubectl delete clusterrole sbd-operator-sbd-agent-scc-user --ignore-not-found=true || true
	@$(MAKE) uninstall || true

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
	go build -o bin/manager cmd/main.go

.PHONY: build-agent
build-agent: manifests generate fmt vet ## Build SBD agent binary.
	go build -o bin/sbd-agent cmd/sbd-agent/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

##@ Container Images

# Primary build targets (Quay-first approach)
# Use these for standard development and CI/CD workflows
# Example: make build-images VERSION=v1.0.0
# Example: make build-push QUAY_REGISTRY=my-registry.io QUAY_ORG=myorg

# PLATFORMS defines the target platforms for multi-platform builds
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le

.PHONY: build-operator-image
build-operator-image: manifests generate fmt vet ## Build operator container image.
	@echo "Building operator image: $(QUAY_OPERATOR_IMG):$(VERSION)"
	$(CONTAINER_TOOL) build -t sbd-operator:$(VERSION) .
	$(CONTAINER_TOOL) tag sbd-operator:$(VERSION) $(QUAY_OPERATOR_IMG):$(VERSION)
	$(CONTAINER_TOOL) tag sbd-operator:$(VERSION) $(QUAY_OPERATOR_IMG):latest

.PHONY: build-agent-image  
build-agent-image: manifests generate fmt vet ## Build agent container image.
	@echo "Building agent image: $(QUAY_AGENT_IMG):$(VERSION)"
	$(CONTAINER_TOOL) build -f Dockerfile.sbd-agent -t sbd-agent:$(VERSION) .
	$(CONTAINER_TOOL) tag sbd-agent:$(VERSION) $(QUAY_AGENT_IMG):$(VERSION)
	$(CONTAINER_TOOL) tag sbd-agent:$(VERSION) $(QUAY_AGENT_IMG):latest

.PHONY: build-images
build-images: build-operator-image build-agent-image ## Build both operator and agent container images.
	@echo "Built SBD Operator images..."
	@echo "Operator: $(QUAY_OPERATOR_IMG):$(VERSION)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(VERSION)"

.PHONY: push-operator-image
push-operator-image: ## Push operator container image to registry.
	@echo "Pushing operator image: $(QUAY_OPERATOR_IMG):$(VERSION)"
	$(CONTAINER_TOOL) push $(QUAY_OPERATOR_IMG):$(VERSION)
	$(CONTAINER_TOOL) push $(QUAY_OPERATOR_IMG):latest

.PHONY: push-agent-image
push-agent-image: ## Push agent container image to registry.
	@echo "Pushing agent image: $(QUAY_AGENT_IMG):$(VERSION)"
	$(CONTAINER_TOOL) push $(QUAY_AGENT_IMG):$(VERSION)
	$(CONTAINER_TOOL) push $(QUAY_AGENT_IMG):latest

.PHONY: push-images
push-images: push-operator-image push-agent-image ## Push both operator and agent container images to registry.
	@echo "Pushed SBD images to registry..."

.PHONY: build-push
build-push: update-manifests build-images push-images ## Build and push both operator and agent images to registry.

.PHONY: buildx
buildx: manifests generate fmt vet ## Build and push multi-platform images to registry.
	@echo "Building and pushing multi-platform SBD Operator images..."
	@echo "Platforms: $(PLATFORMS)"
	@echo "Operator: $(QUAY_OPERATOR_IMG):$(VERSION)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(VERSION)"
	
	# Create buildx builder if it doesn't exist
	- $(CONTAINER_TOOL) buildx create --name sbd-operator-builder
	- $(CONTAINER_TOOL) buildx use sbd-operator-builder
	
	# Build and push operator image (multi-platform)
	@echo "Building and pushing multi-platform operator image..."
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) \
		--tag $(QUAY_OPERATOR_IMG):$(VERSION) \
		--tag $(QUAY_OPERATOR_IMG):latest \
		-f Dockerfile.cross .
	rm Dockerfile.cross
	
	# Build and push agent image (multi-platform)
	@echo "Building and pushing multi-platform agent image..."
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile.sbd-agent > Dockerfile.sbd-agent.cross
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) \
		--tag $(QUAY_AGENT_IMG):$(VERSION) \
		--tag $(QUAY_AGENT_IMG):latest \
		-f Dockerfile.sbd-agent.cross .
	rm Dockerfile.sbd-agent.cross
	
	# Cleanup builder
	- $(CONTAINER_TOOL) buildx rm sbd-operator-builder
	
	@echo "Successfully built and pushed multi-platform images!"

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
	@echo "Operator: $(QUAY_OPERATOR_IMG):$(VERSION)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(VERSION)"
	
	# Update agent daemonset manifests
	@for file in deploy/sbd-agent-daemonset*.yaml; do \
		if [ -f "$$file" ]; then \
			echo "Updating $$file..."; \
			sed -i.bak 's|image: quay\.io/medik8s/sbd-agent:.*|image: $(QUAY_AGENT_IMG):$(VERSION)|g' "$$file"; \
			rm -f "$$file.bak"; \
		fi; \
	done
	
	# Update sample configs
	@for file in config/samples/*.yaml; do \
		if [ -f "$$file" ] && grep -q 'image:' "$$file"; then \
			echo "Updating $$file..."; \
			sed -i.bak 's|image: "quay\.io/medik8s/sbd-agent:.*"|image: "$(QUAY_AGENT_IMG):$(VERSION)"|g' "$$file"; \
			rm -f "$$file.bak"; \
		fi; \
	done
	
	@echo "Manifests updated successfully!"

.PHONY: build-installer
build-installer: update-manifests manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(QUAY_OPERATOR_IMG):$(VERSION)
	$(KUSTOMIZE) build config/default > dist/install.yaml

.PHONY: build-openshift-installer
build-openshift-installer: update-manifests manifests generate kustomize ## Generate a consolidated YAML with CRDs, deployment, and OpenShift SecurityContextConstraints.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(QUAY_OPERATOR_IMG):$(VERSION)
	$(KUSTOMIZE) build config/openshift-default > dist/install-openshift.yaml

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
