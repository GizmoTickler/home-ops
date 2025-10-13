# Project Context

## Purpose
This is a comprehensive home infrastructure repository for a production-grade homelab Kubernetes cluster. The project serves as a learning platform for cloud-native technologies, GitOps practices, and infrastructure automation while running self-hosted applications.

**Core Objectives:**
- Maintain a production-like Kubernetes environment using GitOps principles
- Automate infrastructure provisioning and management with custom tooling
- Implement enterprise-grade patterns (distributed storage, observability, security)
- Provide reproducible infrastructure-as-code for learning and experimentation

## Tech Stack

### Infrastructure Layer
- **Hypervisor**: VMware ESXi with 3 control plane VMs (8 vCPU, 48GB RAM each)
- **Operating System**: Talos Linux v1.11.2 (immutable, minimal, secure)
- **Kubernetes**: v1.34.1 running on Talos
- **Storage Backend**: TrueNAS Scale providing NFS 4.1 over 4x10Gbps LACP

### GitOps & Automation
- **GitOps**: Flux v2.7.0 for continuous delivery
- **Secret Management**: SOPS with age encryption, 1Password via External Secrets Operator
- **Dependency Management**: Renovate for automated updates (container images, Helm charts, manifests)
- **CI/CD**: GitHub Actions for schema validation, testing, and deployment

### Networking & Ingress
- **CNI**: Cilium with eBPF datapath, Gateway API implementation
- **Ingress**: EnvoyProxy for ingress services
- **DNS**: external-dns for Cloudflare and Unifi local DNS, CoreDNS for cluster DNS
- **External Access**: Cloudflare Tunnel via cloudflared

### Storage
- **Distributed Storage**: Rook Ceph v1.15+ for persistent volumes
- **Local Storage**: OpenEBS local-path provisioner
- **Backup/Recovery**: VolSync with Kopia for PVC snapshots and restores
- **Storage Controllers**: Dual NVMe controllers per VM (500GB boot/local + 1TB Ceph)

### Observability
- **Metrics**: Prometheus, Grafana, kube-state-metrics, node-exporter
- **Logging**: Loki with fluentbit for log aggregation
- **Alerting**: Alertmanager with Pushover and iLert integrations
- **Monitoring**: Blackbox Exporter for endpoint monitoring
- **Autoscaling**: KEDA for event-driven autoscaling

### Security & Certificates
- **Certificate Management**: cert-manager with Google Trust Services
- **Secret Injection**: External Secrets Operator with 1Password Connect
- **Secret Encryption**: SOPS with age, encrypted secrets committed to Git

### Custom Tooling (Go-based)
- **HomeOps CLI** (`cmd/homeops-cli/`): Custom Go application built with Cobra
  - **Language**: Go 1.25.1
  - **Template Engine**: Jinja2 templates (via minijinja) with go:embed
  - **Key Libraries**:
    - k8s.io/client-go v0.34.1 for Kubernetes operations
    - github.com/spf13/cobra v1.10.1 for CLI framework
    - github.com/truenas/api_client_golang for TrueNAS API
    - github.com/vmware/govmomi v0.52.0 for vSphere operations
    - go.uber.org/zap for structured logging
    - gopkg.in/yaml.v3 for YAML processing
  - **Capabilities**: Bootstrap, Talos management, VM deployment, VolSync operations

## Project Conventions

### Code Style (Go CLI)

**Formatting:**
- Use `go fmt` for all Go code (enforced via Makefile)
- Run `make fmt` before committing
- Follow standard Go naming conventions:
  - PascalCase for exported identifiers
  - camelCase for unexported identifiers
  - ALL_CAPS for constants

**Project Structure:**
- `cmd/homeops-cli/` - CLI entry point and command definitions
- `cmd/homeops-cli/internal/` - Internal packages (not importable)
  - `internal/templates/` - Embedded Jinja2 templates
  - `internal/common/` - Shared utilities (logger, secrets, git)
  - `internal/errors/` - Error handling and recovery
  - `internal/config/` - Configuration management
  - `internal/talos/`, `internal/truenas/`, `internal/vsphere/` - Service clients
  - `internal/testutil/` - Test helpers and mocks

**Error Handling:**
- Wrap errors with context using `fmt.Errorf("context: %w", err)`
- Use structured logging with `go.uber.org/zap`
- Return errors up the call stack; handle at command level
- Validate inputs early with clear error messages

**Testing:**
- Unit tests in `*_test.go` files alongside source
- Integration tests with `//go:build integration` tag
- Use `testify` for assertions: `github.com/stretchr/testify`
- Mock external services (TrueNAS, vSphere, Kubernetes)
- Target 80% code coverage (enforced in CI)

### Architecture Patterns

**CLI Architecture:**
- **Command Pattern**: Cobra-based commands in `cmd/` subdirectories
- **Embedded Templates**: Templates stored in `internal/templates/` and embedded at compile time
- **Service Layer**: Separate packages for external integrations (Talos factory, TrueNAS API, vSphere)
- **Configuration**: Environment variables via mise (.tool-versions), versions.env for component versions
- **Secret Resolution**: 1Password references resolved during template rendering

**Kubernetes GitOps:**
- **Flux Kustomizations**: Recursive structure in `kubernetes/apps/<namespace>/<app>/`
  - `ks.yaml` - Flux Kustomization pointing to `app/` directory
  - `app/helmrelease.yaml` - HelmRelease resource
  - `app/kustomization.yaml` - Kustomize configuration
  - `app/helm/values.yaml` - Helm chart values
- **Components**: Reusable Kustomize components in `kubernetes/components/`
- **Dependencies**: Explicit dependencies between HelmReleases and Kustomizations
- **SOPS Integration**: Age-encrypted secrets with automatic decryption by Flux

**VM Deployment Pattern:**
1. Generate custom Talos ISO using factory API with schematic.yaml
2. Download ISO to TrueNAS via API
3. Create VM with dual NVMe controllers (ZVol naming: no dashes)
4. Apply Talos configuration using embedded templates with 1Password secret injection

### Testing Strategy

**CLI Testing:**
- **Unit Tests**: Test individual functions with mocks for external dependencies
- **Integration Tests**: Test against real Kubernetes cluster (tagged with `integration`)
- **Coverage Goal**: 80% minimum code coverage
- **Test Commands**:
  - `make test` - Run unit tests (skip integration)
  - `make test-integration` - Run integration tests
  - `make test-coverage` - Generate coverage report
  - `make test-watch` - Watch mode for TDD

**Kubernetes Validation:**
- **Schema Validation**: GitHub Actions with kubeconform
- **YAML Linting**: Pre-commit hooks with yamllint
- **Flux Validation**: `flux diff` before deployment
- **Dry-Run**: Use `--dry-run` flags for CLI operations when available

**Development Workflow:**
```bash
cd cmd/homeops-cli/
make fmt         # Format code
make build       # Build binary
make test        # Run tests
make check       # Run all checks (fmt, vet, lint, test)
make dev         # Quick cycle: fmt, build, test
```

### Git Workflow

**Branch Strategy:**
- **Main Branch**: `main` (production, auto-deploys via Flux)
- **Feature Branches**: `feature/<name>` or `fix/<name>`
- **No Development Branch**: Direct PR to main with CI validation

**Commit Conventions:**
- Use conventional commits format:
  - `feat: Add new Talos VM deployment feature`
  - `fix: Resolve VolSync restore timeout issue`
  - `chore: Update dependencies`
  - `docs: Update README with new CLI commands`
- Keep commits atomic and focused
- Reference issues when applicable: `feat: Add feature (#123)`

**Configuration Change Workflow:**
```bash
# CRITICAL: Always commit and push before reconciling
# Flux requires changes in git to apply them
git add kubernetes/apps/namespace/app/
git commit -m "feat: Update app configuration"
git push

# Then trigger reconciliation
flux reconcile source git flux-system -n flux-system
flux reconcile kustomization <app-name>
```

**Pull Request Requirements:**
- Pass all CI checks (tests, linting, kubeconform validation)
- Include description of changes and testing performed
- Update documentation if adding new features
- Squash commits when merging

## Domain Context

### Talos Linux Specifics
- **Immutable OS**: No SSH access, all configuration via API
- **Configuration**: Machine configs generated from templates with 1Password secrets
- **Upgrades**: Managed via system-upgrade-controller or CLI
- **API Access**: Requires talosconfig file (path in TALOSCONFIG env var)

### Kubernetes Cluster Details
- **Node Count**: 3 control planes (hyper-converged, no separate workers)
- **Pod Network**: 10.42.0.0/16 (Cilium with native routing)
- **Service Network**: 10.43.0.0/16
- **Gateway IPs**: 192.168.123.101-149 (for LoadBalancer services)
- **Internal Domain**: Configured via SECRET_DOMAIN variable

### Storage Architecture
- **Ceph Pool**: Distributed across 3x1TB NVMe vdisks (one per node)
- **OpenEBS**: Local-path storage on 500GB boot vdisk
- **NFS Datastore**: TrueNAS providing VM storage (not used for pods)
- **Backup Strategy**: VolSync with Kopia backing up to TrueNAS

### Secret Management
- **Git Secrets**: SOPS-encrypted with age, committed to repository
- **1Password**: External Secrets Operator syncs to Kubernetes secrets
- **Age Key**: Stored in SOPS_AGE_KEY_FILE environment variable
- **Bootstrap**: CLI validates 1Password connectivity during bootstrap

### Template System
- **Location**: `cmd/homeops-cli/internal/templates/`
- **Format**: Jinja2 syntax with environment variable substitution
- **Embedding**: Templates compiled into binary via go:embed
- **Rendering**: minijinja with 1Password reference resolution
- **Variables**: From environment, versions.env, or CLI flags

## Important Constraints

### Environment Variables
**Required (must be set globally):**
- `KUBECONFIG` - Path to Kubernetes config
- `TALOSCONFIG` - Path to Talos config
- `SOPS_AGE_KEY_FILE` - Path to SOPS age key

**Required for CLI operations:**
- `KUBERNETES_VERSION` - Current K8s version (from versions.env)
- `TALOS_VERSION` - Current Talos version (from versions.env)

**Auto-configured:**
- `MINIJINJA_CONFIG_FILE=./.minijinja.toml` (set by main.go if not defined)

### Tool Requirements
**For CLI development:**
- Go 1.25.1+
- golangci-lint (optional, for linting)
- mise (for environment management)

**For cluster operations:**
- kubectl (Kubernetes CLI)
- talosctl (Talos CLI)
- flux (Flux CLI)
- op (1Password CLI)
- kustomize, helmfile

### Operational Constraints
- **No SSH**: Talos is immutable; no SSH access to nodes
- **GitOps First**: All Kubernetes changes must be in Git before Flux can apply
- **VM Naming**: No dashes in VM names (affects ZVol naming in TrueNAS)
- **ISO Generation**: Always use `--generate-iso` flag for custom Talos ISOs
- **Secret Format**: 1Password references use `op://vault/item/field` format

### Development Constraints
- **Working Directory**: Always run CLI commands from `cmd/homeops-cli/`
- **Embedded Templates**: Template changes require rebuild (`make build`)
- **Test Coverage**: 80% minimum coverage required
- **Pre-commit**: Format and vet checks run automatically

## External Dependencies

### Cloud Services (Critical Path)
- **1Password**: Secret storage and injection (External Secrets Operator)
- **Cloudflare**: DNS management and Cloudflare Tunnel for external access
- **GitHub**: Repository hosting and CI/CD (GitHub Actions)

### External APIs
- **TrueNAS API**: VM creation and ISO management
- **Talos Factory API**: Custom ISO generation with schematic.yaml
- **vSphere API**: (Optional) ESXi VM management via govmomi
- **1Password Connect API**: Secret resolution during template rendering
- **Unifi Controller API**: Internal DNS record updates via external-dns
- **Cloudflare API**: External DNS record updates via external-dns

### Third-Party Services (Non-Critical)
- **Pushover**: Mobile push notifications for alerts (one-time $5 fee)
- **iLert**: Incident management and alerting (free tier)
- **Google Trust Services**: TLS certificate issuance via cert-manager
- **Google Workspace**: Email hosting (personal use)

### Container Registries
- **Primary**: ghcr.io (GitHub Container Registry)
- **Mirror**: Spegel (local cluster mirror for improved pull performance)
- **Alternatives**: docker.io, quay.io (as needed)

### Kubernetes Dependencies
- **Helm Repositories**: Various chart repositories (Cilium, Rook, Prometheus, etc.)
- **OCI Registries**: Helm charts published as OCI artifacts
- **Flux Dependencies**: Git repository as source of truth
