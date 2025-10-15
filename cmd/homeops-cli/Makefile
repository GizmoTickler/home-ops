# HomeOps CLI Makefile
# Provides common development and build tasks

.PHONY: help build test clean install-completion lint fmt vet deps check-deps benchmark coverage run-tests

# Default target
.DEFAULT_GOAL := help

# Variables
BINARY_NAME := homeops-cli
BUILD_DIR := .
GO_FILES := $(shell find . -name '*.go' -type f)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

# Colors for output
RED := \033[31m
GREEN := \033[32m
YELLOW := \033[33m
BLUE := \033[34m
RESET := \033[0m

## help: Show this help message
help:
	@echo "$(BLUE)HomeOps CLI Development Tasks$(RESET)"
	@echo ""
	@echo "$(GREEN)Available targets:$(RESET)"
	@awk 'BEGIN {FS = ":.*##"; printf "\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  $(YELLOW)%-20s$(RESET) %s\n", $$1, $$2 } /^##@/ { printf "\n$(BLUE)%s$(RESET)\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build Tasks

## build: Build the binary
build: check-deps
	@echo "$(GREEN)Building $(BINARY_NAME)...$(RESET)"
	go build $(LDFLAGS) -o $(BINARY_NAME) .
	@echo "$(GREEN)✅ Build complete: $(BINARY_NAME)$(RESET)"

## build-release: Build optimized release binary
build-release: check-deps
	@echo "$(GREEN)Building release version of $(BINARY_NAME)...$(RESET)"
	CGO_ENABLED=0 go build $(LDFLAGS) -a -installsuffix cgo -o $(BINARY_NAME) .
	@echo "$(GREEN)✅ Release build complete: $(BINARY_NAME)$(RESET)"

## install: Install the binary to GOPATH/bin
install: build
	@echo "$(GREEN)Installing $(BINARY_NAME)...$(RESET)"
	go install $(LDFLAGS) .
	@echo "$(GREEN)✅ Installed $(BINARY_NAME) to $(shell go env GOPATH)/bin$(RESET)"

##@ Development Tasks

## test: Run all tests (unit tests only, skip long-running integration tests)
test:
	@echo "$(GREEN)Running tests...$(RESET)"
	go test -v -race -short -timeout 30s ./...
	@echo "$(GREEN)✅ Tests completed$(RESET)"

## test-unit: Run unit tests only
test-unit:
	@echo "$(GREEN)Running unit tests...$(RESET)"
	go test -v -short -race ./...
	@echo "$(GREEN)✅ Unit tests completed$(RESET)"

## test-integration: Run integration tests
test-integration:
	@echo "$(GREEN)Running integration tests...$(RESET)"
	go test -v -tags=integration -timeout 30m ./...
	@echo "$(GREEN)✅ Integration tests completed$(RESET)"

## test-coverage: Run tests with coverage
test-coverage:
	@echo "$(GREEN)Running tests with coverage...$(RESET)"
	go test -v -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)✅ Coverage report generated: coverage.html$(RESET)"
	@echo "$(BLUE)Coverage summary:$(RESET)"
	@go tool cover -func=coverage.out | tail -n 1

## test-coverage-threshold: Check if coverage meets threshold
test-coverage-threshold: test-coverage
	@echo "$(GREEN)Checking coverage threshold (80%)...$(RESET)"
	@coverage=$$(go tool cover -func=coverage.out | grep total | awk '{print $$3}' | sed 's/%//'); \
	if [ $$(echo "$$coverage >= 80" | bc) -eq 1 ]; then \
		echo "$(GREEN)✅ Coverage $$coverage% meets threshold$(RESET)"; \
	else \
		echo "$(RED)❌ Coverage $$coverage% is below 80% threshold$(RESET)"; \
		exit 1; \
	fi

## test-verbose: Run tests with verbose output
test-verbose:
	@echo "$(GREEN)Running tests with verbose output...$(RESET)"
	go test -v -race -count=1 ./...

## test-specific: Run a specific test (usage: make test-specific TEST=TestName)
test-specific:
	@echo "$(GREEN)Running specific test: $(TEST)$(RESET)"
	go test -v -race -run $(TEST) ./...

## test-package: Test a specific package (usage: make test-package PKG=./cmd/talos)
test-package:
	@echo "$(GREEN)Testing package: $(PKG)$(RESET)"
	go test -v -race $(PKG)

## benchmark: Run benchmarks
benchmark:
	@echo "$(GREEN)Running benchmarks...$(RESET)"
	go test -bench=. -benchmem -benchtime=10s ./...
	@echo "$(GREEN)✅ Benchmarks completed$(RESET)"

## benchmark-compare: Compare benchmark results (requires benchstat)
benchmark-compare:
	@echo "$(GREEN)Running benchmark comparison...$(RESET)"
	@if command -v benchstat >/dev/null 2>&1; then \
		go test -bench=. -benchmem -count=10 ./... > new.bench; \
		if [ -f old.bench ]; then \
			benchstat old.bench new.bench; \
		fi; \
		mv new.bench old.bench; \
	else \
		echo "$(YELLOW)⚠️  benchstat not found. Install with: go install golang.org/x/perf/cmd/benchstat@latest$(RESET)"; \
	fi

## test-watch: Run tests in watch mode (requires entr)
test-watch:
	@echo "$(GREEN)Watching for changes and running tests...$(RESET)"
	@if command -v entr >/dev/null 2>&1; then \
		find . -name '*.go' | entr -c go test -v -short ./...; \
	else \
		echo "$(YELLOW)⚠️  entr not found. Install with: brew install entr$(RESET)"; \
	fi

## test-tdd: TDD mode - watch and run tests on changes
test-tdd:
	@echo "$(GREEN)Starting TDD mode...$(RESET)"
	@if command -v entr >/dev/null 2>&1; then \
		find . -name '*.go' | entr -c sh -c 'clear && go test -v -short -failfast ./...'; \
	else \
		echo "$(YELLOW)⚠️  entr not found. Install with: brew install entr$(RESET)"; \
	fi

## test-fuzz: Run fuzz tests
test-fuzz:
	@echo "$(GREEN)Running fuzz tests...$(RESET)"
	go test -fuzz=. -fuzztime=10s ./...

## test-report: Generate detailed test report
test-report:
	@echo "$(GREEN)Generating test report...$(RESET)"
	@mkdir -p test-reports
	go test -v -json ./... > test-reports/test-output.json
	@echo "$(GREEN)✅ Test report generated: test-reports/test-output.json$(RESET)"

## test-clean: Clean test artifacts
test-clean:
	@echo "$(GREEN)Cleaning test artifacts...$(RESET)"
	rm -f coverage.out coverage.html
	rm -rf test-reports/
	rm -f *.bench
	@echo "$(GREEN)✅ Test artifacts cleaned$(RESET)"

## lint: Run golangci-lint
lint:
	@echo "$(GREEN)Running linter...$(RESET)"
	@if [ -x /opt/homebrew/bin/golangci-lint ]; then \
		/opt/homebrew/bin/golangci-lint run; \
	elif command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "$(YELLOW)⚠️  golangci-lint not found. Install with: brew install golangci-lint$(RESET)"; \
	fi

## fmt: Format Go code
fmt:
	@echo "$(GREEN)Formatting code...$(RESET)"
	go fmt ./...
	@echo "$(GREEN)✅ Code formatted$(RESET)"

## vet: Run go vet
vet:
	@echo "$(GREEN)Running go vet...$(RESET)"
	go vet ./...
	@echo "$(GREEN)✅ Vet completed$(RESET)"

## check: Run all checks (fmt, vet, lint, test)
check: fmt vet lint test
	@echo "$(GREEN)✅ All checks passed$(RESET)"

##@ Dependencies

## deps: Download and tidy dependencies
deps:
	@echo "$(GREEN)Downloading dependencies...$(RESET)"
	go mod download
	go mod tidy
	@echo "$(GREEN)✅ Dependencies updated$(RESET)"

## check-deps: Verify dependencies
check-deps:
	@echo "$(GREEN)Checking dependencies...$(RESET)"
	go mod verify

## deps-update: Update all dependencies
deps-update:
	@echo "$(GREEN)Updating dependencies...$(RESET)"
	go get -u ./...
	go mod tidy
	@echo "$(GREEN)✅ Dependencies updated$(RESET)"

##@ Shell Completion

## install-completion: Install shell completion
install-completion: build
	@echo "$(GREEN)Installing shell completion...$(RESET)"
	./install-completion.sh
	@echo "$(GREEN)✅ Shell completion installed$(RESET)"

## generate-completion: Generate completion scripts for all shells
generate-completion: build
	@echo "$(GREEN)Generating completion scripts...$(RESET)"
	mkdir -p completions
	./$(BINARY_NAME) completion bash > completions/$(BINARY_NAME).bash
	./$(BINARY_NAME) completion zsh > completions/$(BINARY_NAME).zsh
	./$(BINARY_NAME) completion fish > completions/$(BINARY_NAME).fish
	./$(BINARY_NAME) completion powershell > completions/$(BINARY_NAME).ps1
	@echo "$(GREEN)✅ Completion scripts generated in ./completions/$(RESET)"

##@ Utility Tasks

## clean: Clean build artifacts
clean:
	@echo "$(GREEN)Cleaning build artifacts...$(RESET)"
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html
	rm -rf completions/
	@echo "$(GREEN)✅ Clean completed$(RESET)"

## version: Show version information
version:
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"

## run: Build and run the application with help
run: build
	@echo "$(GREEN)Running $(BINARY_NAME)...$(RESET)"
	./$(BINARY_NAME) --help

## demo: Run a quick demo of the CLI
demo: build
	@echo "$(GREEN)Running CLI demo...$(RESET)"
	@echo "$(BLUE)Available commands:$(RESET)"
	./$(BINARY_NAME) --help
	@echo ""
	@echo "$(BLUE)Talos commands:$(RESET)"
	./$(BINARY_NAME) talos --help
	@echo ""
	@echo "$(BLUE)Kubernetes commands:$(RESET)"
	./$(BINARY_NAME) k8s --help

## size: Show binary size
size: build
	@echo "$(GREEN)Binary size:$(RESET)"
	@ls -lh $(BINARY_NAME) | awk '{print $$5 "\t" $$9}'

##@ Docker Tasks (if needed)

## docker-build: Build Docker image
docker-build:
	@echo "$(GREEN)Building Docker image...$(RESET)"
	docker build -t homeops-cli:$(VERSION) .
	@echo "$(GREEN)✅ Docker image built: homeops-cli:$(VERSION)$(RESET)"

##@ Development Workflow

## dev: Quick development cycle (fmt, build, test)
dev: fmt build test
	@echo "$(GREEN)✅ Development cycle complete$(RESET)"

## release-prep: Prepare for release (check, build-release, generate-completion)
release-prep: check build-release generate-completion
	@echo "$(GREEN)✅ Release preparation complete$(RESET)"
	@echo "$(BLUE)Binary: $(BINARY_NAME)$(RESET)"
	@echo "$(BLUE)Version: $(VERSION)$(RESET)"
	@echo "$(BLUE)Completion scripts: ./completions/$(RESET)"

# Watch for changes and rebuild (requires entr: brew install entr)
## watch: Watch for changes and rebuild
watch:
	@echo "$(GREEN)Watching for changes... (Press Ctrl+C to stop)$(RESET)"
	@if command -v entr >/dev/null 2>&1; then \
		find . -name '*.go' | entr -c make dev; \
	else \
		echo "$(YELLOW)⚠️  entr not found. Install with: brew install entr$(RESET)"; \
	fi