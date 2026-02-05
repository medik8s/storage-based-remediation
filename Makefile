# IMAGE_REGISTRY used to indicate the registery/group for the operator, bundle and catalog
IMAGE_REGISTRY ?= quay.io/medik8s
export IMAGE_REGISTRY

# Quay registry configuration - primary image naming system
OPERATOR_NAME ?= sbd-operator
AGENT_NAME ?= sbd-agent
QUAY_OPERATOR_NAME ?= $(IMAGE_REGISTRY)/$(OPERATOR_NAME)
QUAY_AGENT_IMG ?= $(IMAGE_REGISTRY)/$(AGENT_NAME)

# VERSION defines the project version for the bundle.
# Update this value when you upgrade the version of your project.
# To re-generate a bundle for another specific version without changing the standard setup, you can:
# - use the VERSION as arg of the bundle target (e.g make bundle VERSION=0.0.2)
# - use environment variables to overwrite this value (e.g export VERSION=0.0.2)
DEFAULT_VERSION := 0.0.1
VERSION ?= $(DEFAULT_VERSION)
PREVIOUS_VERSION ?= $(DEFAULT_VERSION)
export VERSION

# When no version is set, use latest as image tags
ifeq ($(VERSION), $(DEFAULT_VERSION))
IMAGE_TAG = latest
else
IMAGE_TAG = v$(VERSION)
endif
export IMAGE_TAG
# Image URL to use all building/pushing image targets
IMG ?= $(IMAGE_REGISTRY)/sbd-operator:$(IMAGE_TAG)

# BUNDLE_IMG defines the image:tag used for the bundle.
# You can use it as an arg. (E.g make bundle-build BUNDLE_IMG=<some-registry>/<project-name-bundle>:<tag>)
BUNDLE_IMG ?= $(IMAGE_REGISTRY)/sbd-operator-bundle:$(IMAGE_TAG)

# The image tag given to the resulting catalog image (e.g. make catalog-build CATALOG_IMG=example.com/operator-catalog:v0.2.0).
CATALOG_IMG ?= $(IMAGE_REGISTRY)/sbd-operator-catalog:$(IMAGE_TAG)

AGENT_IMG ?= $(IMAGE_REGISTRY)/sbd-agent:$(IMAGE_TAG)

# Build information
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_DESCRIBE ?= $(shell git describe --tags --dirty 2>/dev/null || echo "unknown")

OPERATOR_SHA=$$(podman inspect $(QUAY_OPERATOR_NAME):$(IMAGE_TAG) --format "{{.ID}}" )
AGENT_SHA=$$(podman inspect $(QUAY_AGENT_IMG):$(IMAGE_TAG) --format "{{.ID}}" )
TEST_ARGS ?= ""

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
manifests: controller-gen agent-rbac ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: agent-rbac
agent-rbac: controller-gen ## Generate ClusterRole for SBD Agent with minimal permissions.
	$(CONTROLLER_GEN) rbac:roleName=sbd-agent-role,fileName=sbd_agent_generated_role.yaml paths="./cmd/sbd-agent/..." output:rbac:artifacts:config=config/rbac/

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: go-tidy
go-tidy: # Run go mod tidy - add missing and remove unused modules.
	go mod tidy

.PHONY: go-vendor
go-vendor:  # Run go mod vendor - make vendored copy of dependencies.
	go mod vendor

.PHONY: go-verify
go-verify: go-tidy go-vendor # Run go mod verify - verify dependencies have expected content
	go mod verify

# Check for sorted imports
test-imports: sort-imports
	$(SORT_IMPORTS) .

# Sort imports
fix-imports: sort-imports
	$(SORT_IMPORTS) -w .

.PHONY: test-all
test-all: test test-smoke test-e2e ## Run all tests: unit tests, smoke tests, and e2e tests

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v -E '/(e2e|smoke)') -coverprofile cover.out

.PHONY: sync-test-files
sync-test-files: ## Sync shared configuration files to test directories.
	@chmod +x scripts/sync-test-files.sh
	@scripts/sync-test-files.sh

.PHONY: test-e2e-clean
test-e2e-clean: test-prep test-e2e ## Run e2e tests with complete deployment pipeline.

TEST_ID=$(shell date +'%s')
TEST_HOME=.tests

.PHONY: test-e2e
test-e2e: ginkgo ## Run e2e tests again (assumes operator already deployed).
	@echo "Running e2e tests (operator must be already deployed)..."
	mkdir -p $(TEST_HOME)/$(TEST_ID)
	$(GINKGO) $(TEST_ARGS) test/e2e -- --test-id $(TEST_ID) --artifacts-dir $(TEST_HOME)/$(TEST_ID) | tee $(TEST_HOME)/$(TEST_ID)/execution.log

.PHONY: test-e2e-with-webhooks  
test-e2e-with-webhooks: sync-test-files ## Run e2e tests with webhooks enabled using deployment pipeline.
	@echo "Running e2e tests with webhooks enabled via deployment pipeline..."
	@# The run-tests.sh script handles webhook certificate generation automatically
	@scripts/prep-tests.sh --type e2e --env cluster -v 
	mkdir -p $(TEST_HOME)/$(TEST_ID)
	$(GINKGO) $(TEST_ARGS) test/e2e -- --test-id $(TEST_ID) --artifacts-dir $(TEST_HOME)/$(TEST_ID) | tee $(TEST_HOME)/$(TEST_ID)/execution.log

.PHONY: test-smoke
test-smoke: ginkgo ## Run smoke tests with building images.
	@echo "Running smoke tests with image building..."
	mkdir -p $(TEST_HOME)/$(TEST_ID)
	$(GINKGO) $(TEST_ARGS) test/e2e -- --test-id $(TEST_ID) --artifacts-dir $(TEST_HOME)/$(TEST_ID) | tee $(TEST_HOME)/$(TEST_ID)/execution.log


.PHONY: test-prep
test-prep: build-openshift-installer sync-test-files ## Run smoke tests with building images.
	@scripts/prep-tests.sh --env cluster -v --no-webhooks
# -ginkgo.label-filter="Remediation" 

.PHONY: load-images
load-images:
	@echo "Loading images into CRC..."
	$(CONTAINER_TOOL) save --format docker-archive $(QUAY_OPERATOR_NAME):$(IMAGE_TAG) -o bin/$(OPERATOR_NAME).tar
	$(CONTAINER_TOOL) save --format docker-archive $(QUAY_AGENT_IMG):$(IMAGE_TAG) -o bin/$(AGENT_NAME).tar
	@eval $$(crc podman-env) && $(CONTAINER_TOOL) load -i bin/$(OPERATOR_NAME).tar
	@eval $$(crc podman-env) && $(CONTAINER_TOOL) load -i bin/$(AGENT_NAME).tar

.PHONY: test-smoke-reload
test-smoke-reload:
	@echo "Reloading operator deployment..."
	@eval $$(crc oc-env) && kubectl patch deployment sbd-operator-controller-manager -n sbd-operator-system -p '{"spec":{"template":{"spec":{"containers":[{"name":"manager","image":"$(QUAY_OPERATOR_NAME)@sha256:$$(podman inspect $(QUAY_AGENT_IMG):$(IMAGE_TAG) --format "{{.ID}}"| head -c 12 )","imagePullPolicy":"Never"}]}}}}'
	@eval $$(crc oc-env) && kubectl patch sbdconfig test-config -n sbd-operator-system -p '{"spec":{"image":"$(QUAY_AGENT_IMG)@sha256:$$(podman inspect $(QUAY_AGENT_IMG):$(IMAGE_TAG) --format "{{.ID}}"| head -c 12 )"}}'
	#	OPERATOR_NAME="$(QUAY_OPERATOR_NAME)@sha256:$(OPERATOR_SHA)" \
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

##@ Tools

.PHONY: setup-odf-storage
setup-odf-storage: ## Build the OpenShift Data Foundation setup tool.
	@echo "🔨 Building setup-odf-storage tool..."
	@$(MAKE) -C tools/setup-odf-storage build

.PHONY: setup-shared-storage  
setup-shared-storage: ## Build the shared storage setup tool.
	@echo "🔨 Building setup-shared-storage tool..."
	@$(MAKE) -C tools/setup-shared-storage build

.PHONY: validate-sbd-consistency
validate-sbd-consistency: ## Build the shared storage setup tool.
	@echo "🔨 Building validate-sbd-consistency tool..."
	@$(MAKE) -C tools/validate-sbd-consistency build

.PHONY: build-tools
build-tools: setup-odf-storage setup-shared-storage validate-sbd-consistency

.PHONY: run
run: manifests generate fmt vet webhook-certs ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: run-dev
run-dev: manifests generate fmt vet webhook-certs-staging ## Run a controller from your host with staging certificates.
	@echo "Starting controller with Let's Encrypt staging certificates..."
	@echo "Set LETSENCRYPT_EMAIL environment variable for your email"
	go run ./cmd/main.go --leader-elect=false

.PHONY: run-prod
run-prod: manifests generate fmt vet webhook-certs-letsencrypt ## Run a controller from your host with production certificates.
	@echo "Starting controller with Let's Encrypt production certificates..."
	@echo "Set LETSENCRYPT_EMAIL environment variable for your email"
	go run ./cmd/main.go

.PHONY: webhook-certs
webhook-certs: ## Generate certificates for webhook development (uses Let's Encrypt by default).
	@echo "Generating webhook certificates for development..."
	@chmod +x scripts/generate-webhook-certs.sh
	@scripts/generate-webhook-certs.sh

.PHONY: webhook-certs-letsencrypt
webhook-certs-letsencrypt: ## Generate Let's Encrypt certificates for webhook development.
	@echo "Generating Let's Encrypt certificates for webhook development..."
	@echo "Using domain: sbd-webhook.aws.validatedpatterns.io"
	@chmod +x scripts/generate-webhook-certs.sh
	@USE_LETSENCRYPT=true \
	 WEBHOOK_DOMAIN=sbd-webhook.aws.validatedpatterns.io \
	 LETSENCRYPT_EMAIL=$(LETSENCRYPT_EMAIL) \
	 LETSENCRYPT_STAGING=false \
	 scripts/generate-webhook-certs.sh

.PHONY: webhook-certs-staging
webhook-certs-staging: ## Generate Let's Encrypt staging certificates for webhook development.
	@echo "Generating Let's Encrypt staging certificates for webhook development..."
	@echo "Using domain: sbd-webhook.aws.validatedpatterns.io"
	@chmod +x scripts/generate-webhook-certs.sh
	@USE_LETSENCRYPT=true \
	 WEBHOOK_DOMAIN=sbd-webhook.aws.validatedpatterns.io \
	 LETSENCRYPT_EMAIL=$(LETSENCRYPT_EMAIL) \
	 LETSENCRYPT_STAGING=true \
	 scripts/generate-webhook-certs.sh

.PHONY: webhook-certs-self-signed
webhook-certs-self-signed: ## Generate self-signed certificates for webhook development.
	@echo "Generating self-signed certificates for webhook development..."
	@chmod +x scripts/generate-webhook-certs.sh
	@USE_LETSENCRYPT=false scripts/generate-webhook-certs.sh

.PHONY: clean-webhook-certs
clean-webhook-certs: ## Clean up generated webhook certificates.
	@echo "Cleaning up webhook certificates..."
	@rm -rf /tmp/k8s-webhook-server/serving-certs
	@rm -rf /tmp/letsencrypt
	@echo "✅ Webhook certificates cleaned up."

##@ Container Images

# Primary build targets (Quay-first approach)
# Use these for standard development and CI/CD workflows
# Example: make build-images VERSION=v1.0.0
# Example: make build-push IMAGE_REGISTRY=my-registry.io/myorg

# PLATFORMS defines the target platforms for multi-platform builds
PLATFORMS ?= linux/arm64,linux/amd64 # Others: linux/s390x,linux/ppc64le

.PHONY: build-operator-image
build-operator-image: manifests generate fmt vet ## Build operator container image.
	@echo "Building operator image: $(QUAY_OPERATOR_NAME):$(IMAGE_TAG)"
	@echo "Git version info will be calculated automatically during build"
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: build-agent-image
build-agent-image: manifests generate fmt vet ## Build agent container image.
	@echo "Building agent image: $(QUAY_AGENT_IMG):$(IMAGE_TAG)"
	@echo "Git version info will be calculated automatically during build"
	$(CONTAINER_TOOL) build -f cmd/sbd-agent/Dockerfile -t ${AGENT_IMG} .

.PHONY: build-multiarch-operator-image
build-multiarch-operator-image: manifests generate fmt vet ## Build multi-platform operator container image.
	@echo "Building multi-platform operator image: $(QUAY_OPERATOR_NAME):$(IMAGE_TAG)"
	@echo "Platforms: $(PLATFORMS)"
	@echo "Git version info will be calculated automatically during build"
	$(CONTAINER_TOOL) build --platform=$(PLATFORMS) -t $(QUAY_OPERATOR_NAME):$(IMAGE_TAG) .

.PHONY: build-multiarch-agent-image
build-multiarch-agent-image: manifests generate fmt vet ## Build multi-platform agent container image.
	@echo "Building multi-platform agent image: $(QUAY_AGENT_IMG):$(IMAGE_TAG)"
	@echo "Platforms: $(PLATFORMS)"
	@echo "Git version info will be calculated automatically during build"
	$(CONTAINER_TOOL) build --platform=$(PLATFORMS) -f cmd/sbd-agent/Dockerfile -t $(QUAY_AGENT_IMG):$(IMAGE_TAG) .

.PHONY: build-images
build-images: build-operator-image build-agent-image ## Build both operator and agent container images.
	@echo "Built SBD Operator images..."
	@echo "Operator: $(QUAY_OPERATOR_NAME):$(IMAGE_TAG)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(IMAGE_TAG)"

.PHONY: build-multiarch-images
build-multiarch-images: build-multiarch-operator-image build-multiarch-agent-image ## Build both operator and agent multi-platform container images.
	@echo "Built multi-platform SBD Operator images..."
	@echo "Operator: $(QUAY_OPERATOR_NAME):$(IMAGE_TAG)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(IMAGE_TAG)"
	@echo "Platforms: $(PLATFORMS)"

.PHONY: push-operator-image
push-operator-image: ## Push operator container image to registry.
	@echo "Pushing operator image: ${IMG}"
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: push-agent-image
push-agent-image: ## Push agent container image to registry.
	@echo "Pushing agent image: ${AGENT_IMG}"
	$(CONTAINER_TOOL) push ${AGENT_IMG}

.PHONY: push-images
push-images: push-operator-image push-agent-image ## Push both operator and agent container images to registry.
	@echo "Pushed SBD images to registry..."

.PHONY: push-multiarch-operator-image
push-multiarch-operator-image: ## Push multi-platform operator container image to registry.
	@echo "Pushing multi-platform operator image: $(QUAY_OPERATOR_NAME):$(IMAGE_TAG)"
	$(CONTAINER_TOOL) manifest push $(QUAY_OPERATOR_NAME):$(IMAGE_TAG)

.PHONY: push-multiarch-agent-image
push-multiarch-agent-image: ## Push multi-platform agent container image to registry.
	@echo "Pushing multi-platform agent image: $(QUAY_AGENT_IMG):$(IMAGE_TAG)"
	$(CONTAINER_TOOL) manifest push $(QUAY_AGENT_IMG):$(IMAGE_TAG)

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

##@ Container (composite)

.PHONY: container-build
container-build: docker-build bundle-build ## Build operator, agent, and bundle images

.PHONY: container-push
container-push: docker-push bundle-push catalog-build catalog-push ## Push operator/agent, bundle, and catalog images

.PHONY: container-build-and-push
container-build-and-push: container-build container-push ## Build and push all images (operator, agent, bundle, catalog)


.PHONY: update-manifests
update-manifests: ## Update all manifests to use current QUAY image references (auto-runs with build-push).
	@echo "Updating manifests with image references..."
	@echo "Operator: $(QUAY_OPERATOR_NAME):$(IMAGE_TAG) aka. $(OPERATOR_SHA)"
	@echo "Agent: $(QUAY_AGENT_IMG):$(IMAGE_TAG)  aka. $(AGENT_SHA)"
	
	# Update agent daemonset manifests
	@for file in deploy/sbd-agent-daemonset*.yaml; do \
		if [ -f "$$file" ]; then \
			echo "Updating $$file..."; \
			sed -i.bak 's|image: quay\.io/medik8s/sbd-agent:.*|image: $(QUAY_AGENT_IMG):$(IMAGE_TAG)|g' "$$file"; \
			rm -f "$$file.bak"; \
		fi; \
	done
	
	# Update sample configs
	@for file in config/samples/*.yaml; do \
		if [ -f "$$file" ] && grep -q 'image:' "$$file"; then \
			echo "Updating $$file..."; \
			sed -i.bak 's|image: "quay\.io/medik8s/sbd-agent:.*"|image: "$(QUAY_AGENT_IMG):$(IMAGE_TAG)"|g' "$$file"; \
			rm -f "$$file.bak"; \
		fi; \
	done
	
	@echo "Manifests updated successfully!"

.PHONY: build-installer
build-installer: update-manifests manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(QUAY_OPERATOR_NAME):$(IMAGE_TAG)
	$(KUSTOMIZE) build config/default > dist/install.yaml

.PHONY: build-openshift-installer
build-openshift-installer: update-manifests manifests generate kustomize ## Generate a consolidated YAML with CRDs, deployment, and OpenShift SecurityContextConstraints.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(QUAY_OPERATOR_NAME):$(IMAGE_TAG)
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
KUSTOMIZE = $(KUSTOMIZE_DIR)/$(KUSTOMIZE_VERSION)/kustomize
CONTROLLER_GEN = $(CONTROLLER_GEN_DIR)/$(CONTROLLER_GEN_VERSION)/controller-gen
KUSTOMIZE_DIR ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN_DIR ?= $(LOCALBIN)/controller-gen
ENVTEST = $(ENVTEST_DIR)/$(ENVTEST_VERSION)/setup-envtest
ENVTEST_DIR ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GINKGO ?= $(LOCALBIN)/ginkgo
SORT_IMPORTS_DIR ?= $(LOCALBIN)/sort-imports
OPERATOR_SDK ?= $(LOCALBIN)/operator-sdk
OPM ?= $(LOCALBIN)/opm
YQ_DIR ?= $(LOCALBIN)/yq

## Tool Versions
KUSTOMIZE_VERSION ?= v5@v5.8.0
CONTROLLER_GEN_VERSION ?= v0.18.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= v2.1.0
GINKGO_VERSION ?= v2.22.2
# See https://github.com/slintes/sort-imports/releases for the last version
SORT_IMPORTS_VERSION = v0.3.0

## Specific Tool Binaries
SORT_IMPORTS = $(SORT_IMPORTS_DIR)/$(SORT_IMPORTS_VERSION)/sort-imports

# OLM tooling versions (aligned with other operators)
OPERATOR_SDK_VERSION ?= v1.33.0
OPM_VERSION ?= v1.36.0

# OLM bundle channels/default (aligned defaults)
CHANNELS ?= stable
DEFAULT_CHANNEL ?= stable

# CSV patch helpers
YQ = $(YQ_DIR)/$(YQ_API_VERSION)-$(YQ_VERSION)/yq
YQ_API_VERSION = v4
YQ_VERSION ?= v4.44.1
# 1x1 transparent PNG
ICON_BASE64 ?= iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/wwAAgMBgS+0L8sAAAAASUVORK5CYII=

# Derived bundle metadata opts
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

.PHONY: kustomize
kustomize: ## Download kustomize locally if necessary.
	$(call go-install-tool,$(KUSTOMIZE),$(KUSTOMIZE_DIR),sigs.k8s.io/kustomize/kustomize/$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-install-tool,$(CONTROLLER_GEN),$(CONTROLLER_GEN_DIR),sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION})


.PHONY: envtest ## This library helps write integration tests for your controllers by setting up and starting an instance of etcd and the Kubernetes API server, without kubelet, controller-manager or other components.
envtest: ## Download envtest-setup locally if necessary.
ifneq ($(wildcard $(ENVTEST_DIR)),)
	chmod -R +w $(ENVTEST_DIR)
endif
	$(call go-install-tool,$(ENVTEST),$(ENVTEST_DIR),sigs.k8s.io/controller-runtime/tools/setup-envtest@${ENVTEST_VERSION})

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

.PHONY: ginkgo
ginkgo: $(GINKGO) ## Download ginkgo locally if necessary.
$(GINKGO): $(LOCALBIN)
	$(call go-install-tool,$(GINKGO),github.com/onsi/ginkgo/v2/ginkgo,$(GINKGO_VERSION))

.PHONY: sort-imports
sort-imports: ## Download sort-imports locally if necessary.
	$(call go-install-tool,$(SORT_IMPORTS),$(SORT_IMPORTS_DIR),github.com/slintes/sort-imports@$(SORT_IMPORTS_VERSION))

# go-install-tool will delete old package $2, then 'go install' any package $3 to $1.
define go-install-tool
@[ -f $(1) ]|| { \
	set -e ;\
	rm -rf $(2) ;\
	TMP_DIR=$$(mktemp -d) ;\
	cd $$TMP_DIR ;\
	go mod init tmp ;\
	BIN_DIR=$$(dirname $(1)) ;\
	mkdir -p $$BIN_DIR ;\
	echo "Downloading $(3)" ;\
	GOBIN=$$BIN_DIR GOFLAGS='' go install $(3) ;\
	rm -rf $$TMP_DIR ;\
}
endef

##@ OLM Bundle & Catalog

# CSV path for post-generation edits if needed
CSV ?= ./bundle/manifests/$(OPERATOR_NAME).clusterserviceversion.yaml

.PHONY: bundle
bundle: manifests operator-sdk kustomize yq ## Generate OLM bundle manifests and metadata, then validate
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(IMG)
	$(KUSTOMIZE) build config/default | $(OPERATOR_SDK) generate bundle -q --manifests --metadata --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	$(MAKE) bundle-update
	$(MAKE) bundle-validate

.PHONY: bundle-validate
bundle-validate: operator-sdk ## Validate bundle directory
	$(OPERATOR_SDK) bundle validate ./bundle --select-optional suite=operatorframework

.PHONY: bundle-build
bundle-build: bundle ## Build bundle image
	@echo "Building bundle image: ${BUNDLE_IMG}"
	$(CONTAINER_TOOL) build -f bundle.Dockerfile -t ${BUNDLE_IMG} .

.PHONY: bundle-push
bundle-push: ## Push bundle image
	@echo "Pushing bundle image: ${BUNDLE_IMG}"
	$(CONTAINER_TOOL) push ${BUNDLE_IMG}

.PHONY: catalog-build
catalog-build: opm ## Build a catalog image (single-bundle index)
	@echo "Building catalog image: ${CATALOG_IMG}"
	$(OPM) index add --container-tool $(CONTAINER_TOOL) --tag $(CATALOG_IMG) --bundles $(BUNDLE_IMG)

.PHONY: catalog-push
catalog-push: ## Push catalog image
	$(CONTAINER_TOOL) push ${CATALOG_IMG}

.PHONY: add-replaces-field
add-replaces-field: ## Add replaces to CSV for versioned builds
	@if [ "$(VERSION)" != "latest" ] && [ "$(PREVIOUS_VERSION)" != "$(VERSION)" ] && [ "$(PREVIOUS_VERSION)" != "" ]; then \
		sed -r -i "/  version: $(VERSION)/ a\  replaces: $(OPERATOR_NAME).v$(PREVIOUS_VERSION)" ${CSV} || true ;\
	else \
		echo "Skipping replaces field (VERSION=$(VERSION), PREVIOUS_VERSION=$(PREVIOUS_VERSION))" ;\
	fi

.PHONY: bundle-reset
bundle-reset: ## Reset bundle to default version
	VERSION=0.0.1 $(MAKE) bundle

.PHONY: operator-sdk
operator-sdk: $(OPERATOR_SDK) ## Download operator-sdk locally if necessary.
$(OPERATOR_SDK): $(LOCALBIN)
	@{ \
	set -e ;\
	OS=$$(go env GOOS) && ARCH=$$(go env GOARCH) ;\
	URL="https://github.com/operator-framework/operator-sdk/releases/download/$(OPERATOR_SDK_VERSION)/operator-sdk_$${OS}_$${ARCH}"; \
	echo "Downloading $$URL"; \
	curl -sSLo $(OPERATOR_SDK) "$$URL"; \
	chmod +x $(OPERATOR_SDK); \
	}

.PHONY: opm
opm: $(OPM) ## Download opm locally if necessary.
$(OPM): $(LOCALBIN)
	@{ \
	set -e ;\
	OS=$$(go env GOOS) && ARCH=$$(go env GOARCH) ;\
	URL="https://github.com/operator-framework/operator-registry/releases/download/$(OPM_VERSION)/$${OS}-$${ARCH}-opm"; \
	echo "Downloading $$URL"; \
	curl -sSLo $(OPM) "$$URL"; \
	chmod +x $(OPM); \
	}

.PHONY: yq
yq: ## Download yq locally if necessary.
	$(call go-install-tool,$(YQ),$(YQ_DIR), github.com/mikefarah/yq/$(YQ_API_VERSION)@$(YQ_VERSION))

.PHONY: bundle-update
bundle-update: yq ## Patch CSV with image, icon and minKubeVersion
	@echo "Patching CSV: ${CSV}"
	@# set container image annotation
	$(YQ) -i '.metadata.annotations.containerImage = "$(IMG)"' ${CSV}
	@# ensure icon has data and mediatype
	$(YQ) -i '.spec.icon[0].base64data = "$(ICON_BASE64)"' ${CSV}
	$(YQ) -i '.spec.icon[0].mediatype = "image/png"' ${CSV}
	@# set minimum supported Kubernetes version
	$(YQ) -i '.spec.minKubeVersion = "1.26.0"' ${CSV}

.PHONY: full-gen
full-gen: go-verify manifests  generate manifests fmt bundle fix-imports bundle-reset ## generates all automatically generated content
