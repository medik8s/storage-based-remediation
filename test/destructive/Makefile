# Destructive Test Makefile
# WARNING: These tests will reboot OpenShift nodes!

# Configuration
IMAGE_NAME ?= sbd-destructive-tests
IMAGE_TAG ?= latest
REGISTRY ?= your-registry.com
FULL_IMAGE = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# Target node for testing (MUST be set before running tests)
TARGET_NODE ?= TARGET_NODE_NAME_HERE

# Default target
.PHONY: help
help:
	@echo "⚠️  DESTRUCTIVE TESTS - WILL REBOOT NODES ⚠️"
	@echo ""
	@echo "Available targets:"
	@echo "  build          - Build the Docker image with Go test binaries"
	@echo "  push           - Push image to registry"
	@echo "  update-manifests - Update manifests with registry and target node"
	@echo "  run-panic-test - Run the panic reboot test (REBOOTS NODE!)"
	@echo "  run-watchdog-test - Run the watchdog reboot test (REBOOTS NODE!)"
	@echo "  monitor-panic  - Monitor panic test logs"
	@echo "  monitor-watchdog - Monitor watchdog test logs"
	@echo "  monitor-node   - Monitor target node status"
	@echo "  cleanup        - Clean up test pods and configs"
	@echo "  verify-node    - Verify target node has required capabilities"
	@echo ""
	@echo "Environment variables:"
	@echo "  REGISTRY       - Container registry (default: $(REGISTRY))"
	@echo "  TARGET_NODE    - Node to test (REQUIRED: $(TARGET_NODE))"
	@echo ""
	@echo "⚠️  THESE TESTS WILL REBOOT THE TARGET NODE! ⚠️"

# Build the test image
.PHONY: build
build:
	@echo "Building destructive test image..."
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) -f Dockerfile ../..
	@echo "✅ Built image: $(IMAGE_NAME):$(IMAGE_TAG)"

# Tag and push to registry  
.PHONY: push
push: build
	@echo "Tagging and pushing to registry..."
	docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(FULL_IMAGE)
	docker push $(FULL_IMAGE)
	@echo "✅ Pushed image: $(FULL_IMAGE)"

# Update manifests with registry and target node
.PHONY: update-manifests
update-manifests:
ifeq ($(TARGET_NODE),TARGET_NODE_NAME_HERE)
	@echo "❌ ERROR: TARGET_NODE must be set to actual node name"
	@echo "   Example: make update-manifests TARGET_NODE=worker-1.example.com"
	@exit 1
endif
	@echo "Updating manifests for target node: $(TARGET_NODE)"
	@echo "Updating manifests for registry: $(FULL_IMAGE)"
	sed -i.bak 's|TARGET_NODE_NAME_HERE|$(TARGET_NODE)|g' *.yaml
	sed -i.bak 's|sbd-destructive-tests:latest|$(FULL_IMAGE)|g' *.yaml
	@echo "✅ Updated manifests (backups saved as *.yaml.bak)"

# Verify target node has required capabilities
.PHONY: verify-node
verify-node:
ifeq ($(TARGET_NODE),TARGET_NODE_NAME_HERE)
	@echo "❌ ERROR: TARGET_NODE must be set"
	@exit 1
endif
	@echo "Verifying target node: $(TARGET_NODE)"
	@echo "Checking if node exists..."
	kubectl get node $(TARGET_NODE)
	@echo ""
	@echo "Checking for watchdog device..."
	kubectl debug node/$(TARGET_NODE) --image=registry.redhat.io/ubi9/ubi:latest -- chroot /host ls -la /dev/watchdog* || echo "⚠️  Watchdog device may not be available"
	@echo ""
	@echo "Node is ready for testing"

# Run panic test (DANGEROUS!)
.PHONY: run-panic-test
run-panic-test:
ifeq ($(TARGET_NODE),TARGET_NODE_NAME_HERE)
	@echo "❌ ERROR: TARGET_NODE must be set and manifests updated"
	@echo "   Run: make update-manifests TARGET_NODE=your-node-name"
	@exit 1
endif
	@echo "⚠️  WARNING: About to run PANIC TEST on node $(TARGET_NODE)"
	@echo "⚠️  This will REBOOT THE NODE in 30 seconds!"
	@read -p "Type 'REBOOT' to confirm: " confirm && [ "$$confirm" = "REBOOT" ]
	kubectl apply -f panic-reboot-test.yaml
	@echo "✅ Panic test started! Monitor with: make monitor-panic"

# Run watchdog test (DANGEROUS!)
.PHONY: run-watchdog-test
run-watchdog-test:
ifeq ($(TARGET_NODE),TARGET_NODE_NAME_HERE)
	@echo "❌ ERROR: TARGET_NODE must be set and manifests updated"
	@echo "   Run: make update-manifests TARGET_NODE=your-node-name"
	@exit 1
endif
	@echo "⚠️  WARNING: About to run WATCHDOG TEST on node $(TARGET_NODE)"
	@echo "⚠️  This will REBOOT THE NODE in ~90 seconds!"
	@read -p "Type 'REBOOT' to confirm: " confirm && [ "$$confirm" = "REBOOT" ]
	kubectl apply -f watchdog-reboot-test.yaml
	@echo "✅ Watchdog test started! Monitor with: make monitor-watchdog"

# Monitor test logs
.PHONY: monitor-panic
monitor-panic:
	@echo "Monitoring panic test logs (Ctrl+C to stop)..."
	kubectl logs -f panic-reboot-test || echo "Pod may have terminated due to node reboot"

.PHONY: monitor-watchdog  
monitor-watchdog:
	@echo "Monitoring watchdog test logs (Ctrl+C to stop)..."
	kubectl logs -f watchdog-reboot-test || echo "Pod may have terminated due to node reboot"

# Monitor node status
.PHONY: monitor-node
monitor-node:
ifeq ($(TARGET_NODE),TARGET_NODE_NAME_HERE)
	@echo "❌ ERROR: TARGET_NODE must be set"
	@exit 1
endif
	@echo "Monitoring node status (Ctrl+C to stop)..."
	watch -n 2 "kubectl get node $(TARGET_NODE) && echo '' && kubectl get pods panic-reboot-test watchdog-reboot-test --ignore-not-found"

# Cleanup test resources
.PHONY: cleanup
cleanup:
	@echo "Cleaning up test resources..."
	kubectl delete pod panic-reboot-test watchdog-reboot-test --ignore-not-found
	kubectl delete configmap panic-test-instructions watchdog-test-instructions --ignore-not-found
	kubectl delete pods --field-selector=status.phase=Failed
	@echo "✅ Cleanup completed"

# Check syntax of Go files
.PHONY: check
check:
	@echo "Checking Go syntax..."
	go fmt *.go
	go vet *.go
	@echo "✅ Syntax check passed"

# Full workflow example
.PHONY: test-workflow
test-workflow:
	@echo "⚠️  DESTRUCTIVE TEST WORKFLOW EXAMPLE ⚠️"
	@echo ""
	@echo "1. Set your target node:"
	@echo "   export TARGET_NODE=your-worker-node-name"
	@echo ""
	@echo "2. Build and push image:"
	@echo "   make build push"
	@echo ""
	@echo "3. Update manifests:"
	@echo "   make update-manifests"
	@echo ""  
	@echo "4. Verify node capabilities:"
	@echo "   make verify-node"
	@echo ""
	@echo "5. Run tests (ONE AT A TIME!):"
	@echo "   make run-panic-test"
	@echo "   # Wait for node to reboot and come back"
	@echo "   make run-watchdog-test"
	@echo ""
	@echo "6. Monitor tests:"
	@echo "   make monitor-panic     # or monitor-watchdog"
	@echo "   make monitor-node      # in another terminal"
	@echo ""
	@echo "7. Cleanup:"
	@echo "   make cleanup"

.PHONY: all
all: help 