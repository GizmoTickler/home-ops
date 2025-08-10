# TrueNAS Deep Explorer Makefile
# A tool for deep exploration and documentation of the TrueNAS VM API

# Build variables
BINARY_NAME := truenas-deep-explorer
GO_VERSION := 1.22.2
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Build flags
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(GIT_COMMIT) -X main.buildTime=$(BUILD_TIME)"

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[0;33m
BLUE := \033[0;34m
NC := \033[0m # No Color

.PHONY: help build clean test deps fmt vet lint run demo version

## Help
help: ## Show this help message
	@echo "$(BLUE)TrueNAS Deep Explorer - Available targets:$(NC)"
	@echo ""
	@echo "$(YELLOW)Build Tasks:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / && /Build Tasks/ {found=1; next} found && /^[a-zA-Z_-]+:.*?## / && !/^##/ {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2} /^## / && found {exit}' $(MAKEFILE_LIST)
	@echo ""
	@echo "$(YELLOW)Development Tasks:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / && /Development Tasks/ {found=1; next} found && /^[a-zA-Z_-]+:.*?## / && !/^##/ {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2} /^## / && found {exit}' $(MAKEFILE_LIST)
	@echo ""
	@echo "$(YELLOW)Code Quality Tasks:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / && /Code Quality Tasks/ {found=1; next} found && /^[a-zA-Z_-]+:.*?## / && !/^##/ {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2} /^## / && found {exit}' $(MAKEFILE_LIST)
	@echo ""
	@echo "$(YELLOW)Utility Tasks:$(NC)"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / && /Utility Tasks/ {found=1; next} found && /^[a-zA-Z_-]+:.*?## / && !/^##/ {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2} /^## / && found {exit}' $(MAKEFILE_LIST)
	@echo ""

## Build Tasks
build: deps ## Build the binary
	@echo "$(BLUE)Building $(BINARY_NAME)...$(NC)"
	@go build $(LDFLAGS) -o $(BINARY_NAME) .
	@echo "$(GREEN)✓ Build complete: $(BINARY_NAME)$(NC)"

build-release: deps ## Build optimized release binary
	@echo "$(BLUE)Building release version of $(BINARY_NAME)...$(NC)"
	@CGO_ENABLED=0 go build $(LDFLAGS) -a -installsuffix cgo -o $(BINARY_NAME) .
	@echo "$(GREEN)✓ Release build complete: $(BINARY_NAME)$(NC)"

install: build ## Install binary to $GOPATH/bin
	@echo "$(BLUE)Installing $(BINARY_NAME)...$(NC)"
	@go install $(LDFLAGS) .
	@echo "$(GREEN)✓ Installed $(BINARY_NAME) to $$GOPATH/bin$(NC)"

## Development Tasks
dev: build ## Build and run with sample parameters (requires env vars)
	@echo "$(BLUE)Running $(BINARY_NAME) in development mode...$(NC)"
	@mkdir -p docs
	@if [ -z "$$TRUENAS_HOST" ] || [ -z "$$TRUENAS_API_KEY" ]; then \
		echo "$(RED)Error: TRUENAS_HOST and TRUENAS_API_KEY environment variables are required$(NC)"; \
		echo "$(YELLOW)Set them with: export TRUENAS_HOST=your-host TRUENAS_API_KEY=your-key$(NC)"; \
		exit 1; \
	fi
	@./$(BINARY_NAME) -output "docs/TRUENAS-VM-DEEP-API-dev.md"

run: build ## Build and run with custom parameters (set ARGS variable)
	@echo "$(BLUE)Running $(BINARY_NAME)...$(NC)"
	@mkdir -p docs
	@./$(BINARY_NAME) $(ARGS)

demo: build ## Show help and version information
	@echo "$(BLUE)$(BINARY_NAME) Demo:$(NC)"
	@echo ""
	@echo "$(YELLOW)Version Information:$(NC)"
	@./$(BINARY_NAME) -h || true
	@echo ""
	@echo "$(YELLOW)Usage Example:$(NC)"
	@echo "  ./$(BINARY_NAME) -truenas-host your-host.local -truenas-api-key your-key"
	@echo ""

## Code Quality Tasks
fmt: ## Format Go code
	@echo "$(BLUE)Formatting code...$(NC)"
	@go fmt ./...
	@echo "$(GREEN)✓ Code formatted$(NC)"

vet: ## Run go vet
	@echo "$(BLUE)Running go vet...$(NC)"
	@go vet ./...
	@echo "$(GREEN)✓ go vet passed$(NC)"

lint: ## Run golangci-lint (requires golangci-lint to be installed)
	@echo "$(BLUE)Running golangci-lint...$(NC)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
		echo "$(GREEN)✓ Linting passed$(NC)"; \
	else \
		echo "$(YELLOW)⚠ golangci-lint not installed, skipping...$(NC)"; \
	fi

test: ## Run tests
	@echo "$(BLUE)Running tests...$(NC)"
	@go test -v ./...
	@echo "$(GREEN)✓ Tests passed$(NC)"

test-coverage: ## Run tests with coverage
	@echo "$(BLUE)Running tests with coverage...$(NC)"
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)✓ Coverage report generated: coverage.html$(NC)"

check: fmt vet test ## Run all code quality checks
	@echo "$(GREEN)✓ All checks passed$(NC)"

## Utility Tasks
clean: ## Clean build artifacts
	@echo "$(BLUE)Cleaning build artifacts...$(NC)"
	@rm -f $(BINARY_NAME)
	@rm -f coverage.out coverage.html
	@rm -f docs/TRUENAS-VM-DEEP-API-dev.md
	@echo "$(GREEN)✓ Clean complete$(NC)"

deps: ## Download and verify dependencies
	@echo "$(BLUE)Downloading dependencies...$(NC)"
	@go mod download
	@go mod verify
	@echo "$(GREEN)✓ Dependencies ready$(NC)"

deps-update: ## Update dependencies
	@echo "$(BLUE)Updating dependencies...$(NC)"
	@go get -u ./...
	@go mod tidy
	@echo "$(GREEN)✓ Dependencies updated$(NC)"

version: build ## Show version information
	@echo "$(BLUE)Version Information:$(NC)"
	@echo "Binary: $(BINARY_NAME)"
	@echo "Version: $(VERSION)"
	@echo "Commit: $(GIT_COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo "Go Version: $(GO_VERSION)"

release-prep: clean fmt vet test build-release ## Prepare for release
	@echo "$(GREEN)✓ Release preparation complete$(NC)"
	@echo "$(BLUE)Binary ready: $(BINARY_NAME)$(NC)"

# Default target
.DEFAULT_GOAL := help