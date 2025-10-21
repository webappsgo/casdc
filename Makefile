# CASDC - Complete Active Directory Server Controller
# Simplified Makefile with 4 core targets: build, release, docker, test

# Project configuration
PROJECT_NAME := casdc
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "development")
BUILD_TIME := $(shell date -u +%Y-%m-%d_%H:%M:%S)
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")

# Build configuration
GO := go
CGO_ENABLED := 0
BUILD_DIR := build
LDFLAGS := -ldflags="-w -s \
	-X main.Version=$(VERSION) \
	-X main.BuildTime=$(BUILD_TIME) \
	-X main.GitCommit=$(GIT_COMMIT)"

# Docker configuration
DOCKER_IMAGE := ghcr.io/casapps/$(PROJECT_NAME)
DOCKER_TAG := $(VERSION)

# Test configuration
TEST_TIMEOUT := 10m

.PHONY: all build release docker test clean help

# Default target
all: build

# Help target
help:
	@echo "CASDC Build System - Simplified"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Main Targets:"
	@echo "  build    - Build for all architectures + host binary"
	@echo "  release  - Create release artifacts for GitHub"
	@echo "  docker   - Build and push Docker image to ghcr.io"
	@echo "  test     - Run all tests (unit, integration, e2e)"
	@echo "  clean    - Remove all build artifacts"
	@echo ""
	@echo "Binary naming: $(PROJECT_NAME)-{os}-{arch}"
	@echo "Current version: $(VERSION)"
	@echo ""

# Build for all architectures + host binary
build:
	@echo "Building $(PROJECT_NAME) for all architectures..."
	@mkdir -p $(BUILD_DIR)

	@echo "Building for Linux AMD64..."
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=amd64 $(GO) build \
		$(LDFLAGS) \
		-o $(BUILD_DIR)/$(PROJECT_NAME)-linux-amd64 \
		./cmd/$(PROJECT_NAME)

	@echo "Building for Linux ARM64..."
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=arm64 $(GO) build \
		$(LDFLAGS) \
		-o $(BUILD_DIR)/$(PROJECT_NAME)-linux-arm64 \
		./cmd/$(PROJECT_NAME)

	@echo "Building for Linux ARM..."
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=linux GOARCH=arm GOARM=7 $(GO) build \
		$(LDFLAGS) \
		-o $(BUILD_DIR)/$(PROJECT_NAME)-linux-arm \
		./cmd/$(PROJECT_NAME)

	@echo "Building for host ($(shell go env GOOS)/$(shell go env GOARCH))..."
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) build \
		$(LDFLAGS) \
		-o $(BUILD_DIR)/$(PROJECT_NAME) \
		./cmd/$(PROJECT_NAME)

	@echo ""
	@echo "Build complete! Binaries in $(BUILD_DIR)/"
	@ls -lh $(BUILD_DIR)/$(PROJECT_NAME)*

# Release - create GitHub release artifacts
release: clean test build
	@echo "Creating release artifacts for $(VERSION)..."
	@mkdir -p $(BUILD_DIR)/release

	@echo "Creating tarballs..."
	@cd $(BUILD_DIR) && tar czf release/$(PROJECT_NAME)-$(VERSION)-linux-amd64.tar.gz $(PROJECT_NAME)-linux-amd64
	@cd $(BUILD_DIR) && tar czf release/$(PROJECT_NAME)-$(VERSION)-linux-arm64.tar.gz $(PROJECT_NAME)-linux-arm64
	@cd $(BUILD_DIR) && tar czf release/$(PROJECT_NAME)-$(VERSION)-linux-arm.tar.gz $(PROJECT_NAME)-linux-arm

	@echo "Generating checksums..."
	@cd $(BUILD_DIR)/release && sha256sum *.tar.gz > checksums.txt

	@echo ""
	@echo "Release artifacts ready in $(BUILD_DIR)/release/"
	@ls -lh $(BUILD_DIR)/release/
	@echo ""
	@echo "Checksums:"
	@cat $(BUILD_DIR)/release/checksums.txt

# Docker - build and push to ghcr.io
docker:
	@echo "Building Docker image for $(DOCKER_IMAGE):$(DOCKER_TAG)..."
	@docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG) \
		-t $(DOCKER_IMAGE):latest \
		--target runtime \
		.

	@echo ""
	@echo "Docker image built successfully!"
	@docker images $(DOCKER_IMAGE)

	@echo ""
	@echo "Pushing to ghcr.io..."
	@docker push $(DOCKER_IMAGE):$(DOCKER_TAG)
	@docker push $(DOCKER_IMAGE):latest

	@echo ""
	@echo "Docker images pushed to ghcr.io"
	@echo "  $(DOCKER_IMAGE):$(DOCKER_TAG)"
	@echo "  $(DOCKER_IMAGE):latest"

# Test - run all tests
test:
	@echo "Running all tests..."

	@echo ""
	@echo "1. Running unit tests..."
	@$(GO) test -v -race -timeout $(TEST_TIMEOUT) ./...

	@echo ""
	@echo "2. Running integration tests..."
	@$(GO) test -v -race -timeout $(TEST_TIMEOUT) -tags=integration ./...

	@echo ""
	@echo "3. Running end-to-end tests..."
	@$(GO) test -v -timeout $(TEST_TIMEOUT) -tags=e2e ./tests/e2e/...

	@echo ""
	@echo "4. Generating coverage report..."
	@mkdir -p $(BUILD_DIR)/coverage
	@$(GO) test -race -coverprofile=$(BUILD_DIR)/coverage/coverage.out -covermode=atomic ./...
	@$(GO) tool cover -html=$(BUILD_DIR)/coverage/coverage.out -o $(BUILD_DIR)/coverage/coverage.html
	@$(GO) tool cover -func=$(BUILD_DIR)/coverage/coverage.out | grep total

	@echo ""
	@echo "All tests complete! Coverage report: $(BUILD_DIR)/coverage/coverage.html"

# Clean - remove all build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR)
	@$(GO) clean -cache
	@echo "Clean complete"