# Makefile Usage

This project includes a comprehensive Makefile to streamline development and build tasks.

## Quick Start

```bash
# Show all available targets
make help

# Build the binary
make build

# Run development cycle (format, build, test)
make dev

# Run a quick demo
make demo
```

## Common Tasks

### Building
- `make build` - Build the binary with version info
- `make build-release` - Build optimized release binary
- `make install` - Install to GOPATH/bin

### Development
- `make dev` - Quick development cycle (fmt, build, test)
- `make test` - Run all tests
- `make test-coverage` - Run tests with coverage report
- `make check` - Run all checks (fmt, vet, lint, test)
- `make watch` - Watch for changes and rebuild (requires `entr`)

### Code Quality
- `make fmt` - Format Go code
- `make vet` - Run go vet
- `make lint` - Run golangci-lint (if installed)

### Dependencies
- `make deps` - Download and tidy dependencies
- `make deps-update` - Update all dependencies

### Shell Completion
- `make install-completion` - Install shell completion
- `make generate-completion` - Generate completion scripts for all shells

### Utilities
- `make clean` - Clean build artifacts
- `make version` - Show version information
- `make size` - Show binary size
- `make run` - Build and run with help

### Release Preparation
- `make release-prep` - Complete release preparation (check, build-release, generate-completion)

## Features

- **Colored Output**: Uses colors for better readability
- **Version Information**: Automatically embeds git version, commit, and build time
- **Dependency Management**: Handles Go module operations
- **Shell Completion**: Integrates with the completion system
- **Development Workflow**: Streamlined development tasks
- **Release Ready**: Optimized release builds

## Requirements

- Go 1.22.2 or later
- Optional: `golangci-lint` for linting
- Optional: `entr` for watch mode (`brew install entr`)

## Examples

```bash
# Complete development workflow
make dev

# Prepare for release
make release-prep

# Install with completion
make install install-completion

# Clean and rebuild
make clean build
```