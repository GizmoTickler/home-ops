# TrueNAS Deep Explorer Migration

## Overview

The `truenas-deep-explorer` tool has been extracted from the `scripts` directory and moved to its own standalone project to preserve this functionality before archiving the scripts directory.

## What was moved

The `truenas-deep-explorer` tool has been extracted from `scripts/cmd/truenas-deep-explorer/` and moved to its own standalone project at the repository root. This includes:

- **Core functionality**: Deep API exploration and documentation generation
- **TrueNAS package**: Local `truenas` package with `WorkingClient` for API interaction
- **Dependencies**: Proper Go module with required dependencies
- **Build system**: Comprehensive Makefile with build, test, and development targets
- **Documentation**: Complete README with usage instructions and examples
- **1Password Integration**: Secure credential management using 1Password CLI (same as homeops-cli)

## New Location Structure

```
truenas-deep-explorer/
├── main.go                 # Main application code
├── truenas/               # TrueNAS API client package
│   └── working_client.go  # TrueNAS API client implementation
├── go.mod                 # Go module definition
├── Makefile              # Build and development tasks
└── README.md             # Documentation and usage guide
```

## Purpose

The `truenas-deep-explorer` is a specialized development/documentation tool that:

- Systematically explores 20+ TrueNAS API methods
- Collects real VM and dataset examples from TrueNAS systems
- Generates comprehensive API documentation in Markdown format
- Aids in understanding TrueNAS API structure and requirements

## Why it was separated

This tool serves a different purpose than the operational VM management functionality that was migrated to `homeops-cli`:

- **homeops-cli**: Production tool for managing VMs and infrastructure
- **truenas-deep-explorer**: Development tool for API exploration and documentation

Since the `scripts` directory is being archived as part of the shell-to-Go migration, this tool was preserved separately to maintain its utility for future development work.

## Key improvements

1. **Standalone project**: No longer dependent on the scripts directory structure
2. **Proper Go module**: Clean dependency management with `go.mod` and `go.sum`
3. **Comprehensive build system**: Makefile with multiple targets for development and release
4. **Better documentation**: Detailed README with usage examples and API method documentation
5. **Preserved functionality**: All original deep exploration capabilities maintained
6. **Enhanced security**: 1Password integration for secure credential management
7. **Consistent patterns**: Uses same credential handling as homeops-cli project

## Usage

### With 1Password (Recommended)

```bash
# Ensure 1Password CLI is authenticated
op whoami

# Configure secrets in 1Password:
# Vault: Infrastructure
# Item: talosdeploy
# Fields: TRUENAS_HOST, TRUENAS_API

cd truenas-deep-explorer
make build
./truenas-deep-explorer  # Credentials automatically retrieved
```

### With Environment Variables or Flags

```bash
cd truenas-deep-explorer
make build

# Using environment variables
export TRUENAS_HOST="your-host"
export TRUENAS_API_KEY="your-key"
./truenas-deep-explorer

# Using command-line flags
./truenas-deep-explorer -truenas-host "your-host" -truenas-api-key "your-key"
```

See the [truenas-deep-explorer README](./truenas-deep-explorer/README.md) for detailed usage instructions.

## Relationship to homeops-cli

While `homeops-cli` includes operational TrueNAS VM management commands (`homeops talos deploy-vm`, `homeops talos manage-vm`, etc.), it does **not** include the deep API exploration functionality. The two tools serve complementary but distinct purposes:

- Use `homeops-cli` for day-to-day VM operations
- Use `truenas-deep-explorer` for API research and documentation