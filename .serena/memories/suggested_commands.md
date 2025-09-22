# Suggested Commands for HomeOps CLI Development

## Essential Build and Test Commands
```bash
# Quick development cycle
make dev           # Format, build, test

# Build and test
make build         # Build the binary
make test          # Run all tests
make check         # Run all checks (fmt, vet, lint, test)

# Testing specific targets
make test-unit            # Run unit tests only
make test-integration     # Run integration tests  
make test-coverage        # Run tests with coverage
make test-coverage-threshold  # Check 80% coverage threshold
make test-package PKG=./cmd/talos  # Test specific package
make test-specific TEST=TestName   # Run specific test

# Code quality
make fmt           # Format code
make vet           # Run go vet
make lint          # Run golangci-lint

# Dependencies
make deps          # Download and tidy dependencies
make deps-update   # Update all dependencies
```

## CLI Usage Examples
```bash
# Bootstrap entire cluster
./homeops-cli bootstrap

# Talos operations
./homeops-cli talos apply-node --ip 192.168.122.10
./homeops-cli talos deploy-vm --name test_node --generate-iso
./homeops-cli talos upgrade-k8s

# Kubernetes operations  
./homeops-cli k8s browse-pvc --namespace default
./homeops-cli k8s restart-deployments --namespace flux-system

# Volume sync operations
./homeops-cli volsync snapshot --pvc data-pvc --namespace default
./homeops-cli volsync restore --pvc data-pvc --namespace default
```

## Development Workflow
```bash
# Standard development cycle
cd cmd/homeops-cli/
make dev    # Format, build, test

# Watch mode for TDD
make test-watch   # Requires entr (brew install entr)
make test-tdd     # TDD mode with clear screen

# Benchmarking
make benchmark    # Run benchmarks
make benchmark-compare  # Compare with previous (requires benchstat)
```

## System Commands (macOS)
```bash
# File operations
ls -la            # List files with details
find . -name "*.go" -type f  # Find Go files
grep -r "pattern" .          # Search in files

# Git operations
git status
git add -A
git commit -m "message"
git push

# Package management
brew install entr         # For file watching
brew install golangci-lint # For linting
```