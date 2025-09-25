# HomeOps CLI Architecture and Code Analysis

## Project Overview
HomeOps CLI is a comprehensive infrastructure management tool for home lab environments, focusing on Talos Linux clusters, Kubernetes applications, and GitOps workflows.

## Architecture

### Command Structure
- **Cobra-based CLI** with hierarchical command organization
- Main entry point in `main.go` with version/commit/build time injection
- Command groups:
  - `bootstrap` - Complete cluster initialization
  - `talos` - Talos Linux node management
  - `kubernetes` (k8s) - K8s cluster operations  
  - `volsync` - Volume backup/restore
  - `workstation` - Development environment setup
  - `completion` - Shell completions

### Core Features

#### Bootstrap Command
- **Preflight Checks**: Validates prerequisites before cluster setup
- **Talos Config Management**: Applies configurations to nodes
- **1Password Integration**: Resolves secrets during template rendering
- **Resource Deployment**: Applies CRDs and initial resources
- **Helm Integration**: Uses Helmfile for dependency management

#### Talos Commands
- `apply-node` - Apply configs to specific nodes
- `upgrade-node` - Upgrade Talos on nodes
- `upgrade-k8s` - Upgrade Kubernetes version
- `deploy-vm` - VM deployment with TrueNAS/vSphere
- `prepare-iso` - Generate custom Talos ISOs

#### Kubernetes Commands  
- `browse-pvc` - Interactive PVC browser
- `node-shell` - Access node shells
- `sync-secrets` - Secret synchronization
- `cleanse-pods` - Pod cleanup operations
- `upgrade-arc` - GitHub ARC runner upgrades

### Internal Packages

#### Core Utilities
- **common/** - Shared utilities (logger, secrets, 1Password auth)
- **config/** - Configuration management and version handling
- **errors/** - Structured error types with recovery strategies
- **templates/** - Embedded template system using go:embed

#### Infrastructure Integrations
- **talos/** - Talos factory API for custom ISOs
- **truenas/** - TrueNAS API client for VM management
- **vsphere/** - vSphere client for virtualization
- **ssh/** - SSH client for remote operations
- **iso/** - ISO download and management

#### Development Support
- **testing/** - Test framework and utilities
- **testutil/** - Mock helpers and test utilities
- **metrics/** - Performance metrics collection
- **security/** - Security cache and validation
- **migration/** - Migration tools

## Coding Patterns and Style

### Error Handling
- Custom `HomeOpsError` type with rich context
- Error types: Template, Kubernetes, Talos, Validation, Network, Config, Security, FileSystem, NotFound
- Consistent error wrapping with `fmt.Errorf`
- User-friendly error messages with recovery hints

### Logging
- Dual logging system: `ColorLogger` for CLI output, `StructuredLogger` for debugging
- Color-coded output (Green for success, Yellow for warnings, Red for errors)
- Log levels: Debug, Info, Warn, Error
- Environment-based log level configuration

### Template System
- Embedded templates using `go:embed` directive
- Jinja2 templates for Talos configs
- Go templates for Helm values
- 1Password reference resolution (`op://vault/item/field`)
- Dynamic template rendering with environment variables

### Configuration Management
- YAML-based configuration with merging support
- Environment variable injection
- Version management from external files
- Talosctl integration for config operations

### Testing Patterns
- Table-driven tests with subtests
- Mocking using interfaces
- Integration tests with build tags
- Benchmarks for performance-critical paths
- Test fixtures in `testdata/`
- Coverage target: 80% (currently ~33%)

## Development Workflow

### Makefile Targets
- **Build**: `make build`, `make build-release`
- **Testing**: `make test`, `make test-coverage`, `make test-integration`
- **Development**: `make dev` (fmt, build, test cycle)
- **Quality**: `make check` (fmt, vet, lint, test)
- **TDD Mode**: `make test-tdd` (watch mode)
- **Dependencies**: `make deps`, `make deps-update`

### Code Quality Tools
- **golangci-lint** with minimal config (errcheck, govet, ineffassign, staticcheck, unused)
- **go fmt** for formatting
- **go vet** for static analysis
- **Benchmarks** for performance testing

## Architectural Patterns

### Command Pattern
- Commands as separate functions returning `*cobra.Command`
- Flag registration with validation and completion
- RunE for error handling
- Subcommand composition

### Embedded Resources
- Templates embedded at compile time
- No external file dependencies for templates
- Version-controlled template files

### Dependency Injection
- Logger instances passed to functions
- Configuration passed as structs
- Interfaces for external services (TrueNAS, vSphere)

### Retry Patterns
- Network operations with exponential backoff
- Configurable retry counts and delays
- Context-aware cancellation

### Security Patterns
- 1Password integration for secrets
- No hardcoded credentials
- Secret validation before use
- Secure template rendering

## Key Design Decisions

1. **Embedded Templates**: All templates compiled into binary for portability
2. **1Password First**: Secrets management through 1Password
3. **Talosctl Integration**: Leverage existing Talos tooling
4. **Color Output**: Enhanced CLI UX with colored output
5. **Comprehensive Bootstrap**: Single command cluster initialization
6. **GitOps Ready**: Designed for Flux-based deployments

## Testing Strategy

### Test Types
- Unit tests for isolated functions
- Integration tests for command flows
- Benchmark tests for performance
- Table-driven tests for comprehensive coverage

### Test Organization
- Tests alongside implementation files
- Shared test utilities in `testutil/`
- Test fixtures in `testdata/`
- Mock implementations for external services

## Deployment Workflow

1. **Development**: Local testing with `make dev`
2. **CI/CD**: Automated testing on commits
3. **Release**: Build optimized binary with `make build-release`
4. **Installation**: Direct binary installation or via go install
5. **Completion**: Shell completion setup via `install-completion.sh`