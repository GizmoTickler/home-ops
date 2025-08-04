# Makefile for TrueNAS VM Tools

.PHONY: all build clean deps test

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Binary names
DEPLOY_BINARY=deploy-truenas-vm
MANAGE_BINARY=manage-truenas-vm
DEEP_EXPLORER_BINARY=truenas-deep-explorer
TEST_CLIENT_BINARY=test-working-client

# Build directory
BUILD_DIR=.

all: deps build

build: build-deploy build-manage build-explorer build-deep-explorer build-test-client

build-deploy:
	@echo "Building $(DEPLOY_BINARY)..."
	$(GOBUILD) -o $(BUILD_DIR)/$(DEPLOY_BINARY) ./cmd/deploy-truenas-vm

build-manage:
	@echo "Building $(MANAGE_BINARY)..."
	$(GOBUILD) -o $(BUILD_DIR)/$(MANAGE_BINARY) ./cmd/manage-truenas-vm

build-deep-explorer:
	@echo "Building $(DEEP_EXPLORER_BINARY)..."
	$(GOBUILD) -o $(BUILD_DIR)/$(DEEP_EXPLORER_BINARY) ./cmd/truenas-deep-explorer

build-test-client:
	@echo "Building $(TEST_CLIENT_BINARY)..."
	$(GOBUILD) -o $(BUILD_DIR)/$(TEST_CLIENT_BINARY) ./cmd/test-working-client

deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -f $(BUILD_DIR)/$(DEPLOY_BINARY)
	rm -f $(BUILD_DIR)/$(MANAGE_BINARY)
	rm -f $(BUILD_DIR)/$(DEEP_EXPLORER_BINARY)
	rm -f $(BUILD_DIR)/$(TEST_CLIENT_BINARY)

test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

# Install dependencies if not present
install-deps:
	@echo "Installing Go dependencies..."
	$(GOGET) github.com/gorilla/websocket@v1.5.1
	$(GOGET) github.com/google/uuid@v1.6.0
	$(GOGET) github.com/PuerkitoBio/goquery@v1.8.1
	$(GOGET) github.com/truenas/api_client_golang/truenas_api@latest

# Build for multiple platforms
build-all: build-linux build-darwin build-windows

build-linux:
	@echo "Building for Linux..."
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(DEPLOY_BINARY)-linux-amd64 ./cmd/deploy-truenas-vm
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(MANAGE_BINARY)-linux-amd64 ./cmd/manage-truenas-vm

build-darwin:
	@echo "Building for macOS..."
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(DEPLOY_BINARY)-darwin-amd64 ./cmd/deploy-truenas-vm
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(MANAGE_BINARY)-darwin-amd64 ./cmd/manage-truenas-vm
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o $(BUILD_DIR)/$(DEPLOY_BINARY)-darwin-arm64 ./cmd/deploy-truenas-vm
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o $(BUILD_DIR)/$(MANAGE_BINARY)-darwin-arm64 ./cmd/manage-truenas-vm

build-windows:
	@echo "Building for Windows..."
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(DEPLOY_BINARY)-windows-amd64.exe ./cmd/deploy-truenas-vm
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o $(BUILD_DIR)/$(MANAGE_BINARY)-windows-amd64.exe ./cmd/manage-truenas-vm

# Development helpers
dev-deploy: build-deploy
	@echo "Built $(DEPLOY_BINARY) for development"

dev-manage: build-manage
	@echo "Built $(MANAGE_BINARY) for development"

# Show help
help:
	@echo "Available targets:"
	@echo "  all          - Download dependencies and build all binaries"
	@echo "  build        - Build all binaries"
	@echo "  build-deploy - Build deploy-truenas-vm binary"
	@echo "  build-manage - Build manage-truenas-vm binary"
	@echo "  deps         - Download and tidy Go dependencies"
	@echo "  clean        - Clean build artifacts"
	@echo "  test         - Run tests"
	@echo "  build-all    - Build for all platforms"
	@echo "  help         - Show this help message"
