.PHONY: build clean test help install

# Tool configuration
TOOL_NAME := $(shell basename $$PWD)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Build configuration
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
CGO_ENABLED := 0

# Directories
BUILD_DIR := ../../bin
DIST_DIR := ../../dist

# Linker flags
LDFLAGS := -s -w
LDFLAGS += -X main.version=$(VERSION)
LDFLAGS += -X main.buildDate=$(BUILD_DATE)
LDFLAGS += -X main.gitCommit=$(COMMIT)

## build: Build the ODF setup tool
build:
	@echo "🔨 Building $(TOOL_NAME)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "$(LDFLAGS)" \
		-o $(BUILD_DIR)/$(TOOL_NAME) \
		.
	@echo "✅ Built $(BUILD_DIR)/$(TOOL_NAME)"

## build-all: Build for multiple platforms
build-all:
	@echo "🔨 Building $(TOOL_NAME) for multiple platforms..."
	@mkdir -p $(DIST_DIR)
	@for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			if [ "$$os" = "windows" ] && [ "$$arch" = "arm64" ]; then continue; fi; \
			ext=""; \
			if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
			echo "Building $$os/$$arch..."; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build \
				-ldflags "$(LDFLAGS)" \
				-o $(DIST_DIR)/$(TOOL_NAME)-$$os-$$arch$$ext \
				.; \
		done; \
	done
	@echo "✅ Built all platforms in $(DIST_DIR)/"

## test: Run tests
test:
	@echo "🧪 Running tests..."
	go test -v ./...
	@echo "✅ Tests completed"

## test-integration: Run integration tests (requires cluster)
test-integration:
	@echo "🧪 Running integration tests..."
	@if ! kubectl cluster-info >/dev/null 2>&1; then \
		echo "❌ No Kubernetes cluster available"; \
		exit 1; \
	fi
	go test -v -tags=integration ./...
	@echo "✅ Integration tests completed"

## install: Install the tool to local bin directory
install: build
	@echo "📦 Installing $(TOOL_NAME)..."
	@mkdir -p ~/.local/bin
	cp $(BUILD_DIR)/$(TOOL_NAME) ~/.local/bin/
	@echo "✅ Installed to ~/.local/bin/$(TOOL_NAME)"
	@echo "💡 Make sure ~/.local/bin is in your PATH"

## clean: Clean build artifacts
clean:
	@echo "🧹 Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)/$(TOOL_NAME)
	rm -rf $(DIST_DIR)/$(TOOL_NAME)-*
	@echo "✅ Cleaned"

## fmt: Format Go code
fmt:
	@echo "🎨 Formatting code..."
	go fmt ./...
	@echo "✅ Code formatted"

## lint: Run linter
lint:
	@echo "🔍 Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "⚠️ golangci-lint not found, running go vet instead"; \
		go vet ./...; \
	fi
	@echo "✅ Linting completed"

## check: Run all checks (fmt, lint, test)
check: fmt lint test
	@echo "✅ All checks passed"

## demo: Run demo setup (dry-run mode)
demo:
	@echo "🎬 Running ODF setup demo..."
	@if [ -f $(BUILD_DIR)/$(TOOL_NAME) ]; then \
		$(BUILD_DIR)/$(TOOL_NAME) --dry-run --verbose; \
	else \
		echo "❌ Tool not built. Run 'make build' first"; \
		exit 1; \
	fi

## help: Show this help message
help:
	@echo "OpenShift Data Foundation Setup Tool"
	@echo "===================================="
	@echo ""
	@echo "Available targets:"
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'
	@echo ""
	@echo "Examples:"
	@echo "  make build                 # Build the tool"
	@echo "  make test                  # Run tests"
	@echo "  make demo                  # Run dry-run demo"
	@echo "  make install               # Install to local bin"
	@echo ""
	@echo "Tool usage:"
	@echo "  $(BUILD_DIR)/$(TOOL_NAME) --help"

## version: Show version information
version:
	@echo "Tool: $(TOOL_NAME)"
	@echo "Version: $(VERSION)"
	@echo "Build Date: $(BUILD_DATE)"
	@echo "Git Commit: $(COMMIT)"
	@echo "Go Version: $(shell go version)"
	@echo "Platform: $(GOOS)/$(GOARCH)"

# Default target
all: check build
	@echo "✅ All tasks completed" 
