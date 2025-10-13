<!-- OPENSPEC:START -->
# OpenSpec Instructions

These instructions are for AI assistants working in this project.

Always open `@/openspec/AGENTS.md` when the request:
- Mentions planning or proposals (words like proposal, spec, change, plan)
- Introduces new capabilities, breaking changes, architecture shifts, or big performance/security work
- Sounds ambiguous and you need the authoritative spec before coding

Use `@/openspec/AGENTS.md` to learn:
- How to create and apply change proposals
- Spec format and conventions
- Project structure and guidelines

Keep this managed block so 'openspec update' can refresh the instructions.

<!-- OPENSPEC:END -->

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This is a comprehensive home infrastructure repository containing:
- **Kubernetes cluster configuration** using GitOps with Flux
- **Talos Linux** cluster management on TrueNAS VMs
- **HomeOps CLI** (Go-based) for infrastructure automation
- **Kubernetes applications** deployed via Helm and Kustomize

## IMPORTANT: Git Workflow

**ALWAYS commit and push changes to git when making configuration changes:**
- Any changes to YAML files in `kubernetes/` directory must be committed and pushed
- Flux GitOps requires changes to be in the git repository to reconcile
- Running `flux reconcile` alone is NOT sufficient - changes must be in git first

```bash
# Standard workflow for configuration changes:
git add -A
git commit -m "feat/fix: descriptive message"
git push
flux reconcile source git flux-system -n flux-system
```

## Essential Commands

### Go CLI Development (cmd/homeops-cli/)

The repository contains a custom CLI tool written in Go. Always work from the `cmd/homeops-cli/` directory:

```bash
cd cmd/homeops-cli/

# Build and test (essential during development)
make build
make test
make check  # Run all checks (fmt, vet, lint, test)

# Development workflow
make dev    # Quick cycle: fmt, build, test
make fmt    # Format code
make lint   # Run linter (requires golangci-lint)

# Dependencies
make deps         # Download and tidy dependencies
make deps-update  # Update all dependencies
```

### Environment Setup

The CLI relies on several environment variables. The following must be set globally:
- `KUBECONFIG` - Path to your Kubernetes config file
- `TALOSCONFIG` - Path to your Talos config file
- `SOPS_AGE_KEY_FILE` - Path to your SOPS age key file

The following is automatically set by main.go if not already defined:
- `MINIJINJA_CONFIG_FILE=./.minijinja.toml`

Required environment variables:
- `KUBERNETES_VERSION` - Current Kubernetes version
- `TALOS_VERSION` - Current Talos Linux version

### Kubernetes Management

**IMPORTANT:** KUBECONFIG is set as a global environment variable. Always use kubectl and flux commands directly without specifying kubeconfig path.

```bash
# Correct way - KUBECONFIG is globally set
kubectl <command>
flux <command>

# NEVER use these patterns since KUBECONFIG is global:
# kubectl --kubeconfig=./kubeconfig <command>  # WRONG
# flux --kubeconfig=./kubeconfig <command>     # WRONG
```

### CLI Usage Examples

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
**Always run format, type-check, and test before completing any task.**

### Memory & Knowledge System
- **Markdown-based storage** in `.serena/memories/` directories
- **Project-specific knowledge** persistence across sessions
- **Contextual retrieval** based on relevance
- **Onboarding support** for new projects

## Architecture Overview

### CLI Architecture (cmd/homeops-cli/)

The HomeOps CLI is structured as a Cobra-based application with the following key components:

**Main Commands:**
- `bootstrap/` - Complete cluster bootstrap with preflight checks
- `talos/` - Talos Linux node and VM management
- `kubernetes/` - Kubernetes cluster operations
- `volsync/` - Volume backup and restore operations
- `workstation/` - Local development environment setup
- `completion/` - Shell completion support

**Internal Packages:**
- `internal/templates/` - Embedded Jinja2 templates for Talos configs
- `internal/talos/` - Talos factory API integration for custom ISOs
- `internal/truenas/` - TrueNAS API client for VM management
- `internal/yaml/` - YAML processing and merging utilities
- `internal/ssh/` - SSH client for remote operations
- `internal/iso/` - ISO download and management

**Template System:**
- Templates are embedded in the binary via go:embed
- Jinja2 templates for Talos configurations in `internal/templates/talos/`
- Bootstrap templates for initial cluster resources
- 1Password integration for secret injection during template rendering

**VM Deployment Flow:**
1. Generate custom Talos ISO using factory API and schematic.yaml
2. Download ISO to TrueNAS storage
3. Create VM with proper ZVol naming convention
4. Apply Talos configuration using embedded templates

### Kubernetes GitOps Structure

**Flux Workflow:**
- `kubernetes/flux/` - Flux system configuration
- `kubernetes/apps/` - Applications organized by namespace
- `kubernetes/components/` - Reusable Kustomize components

**Application Structure Pattern:**
```
kubernetes/apps/<namespace>/<app>/
├── app/
│   ├── helmrelease.yaml      # Helm deployment
│   ├── kustomization.yaml    # Kustomize configuration
│   └── helm/values.yaml      # Helm values
└── ks.yaml                   # Flux Kustomization
```

**Key Namespaces:**
- `flux-system` - Flux controllers
- `kube-system` - Core Kubernetes components (Cilium, CoreDNS)
- `cert-manager` - Certificate management
- `external-secrets` - 1Password integration
- `observability` - Grafana, Prometheus, Loki stack
- `default` - Media applications and productivity tools

### Infrastructure Components

**Storage:**
- Rook Ceph for distributed storage
- OpenEBS for local persistent volumes
- VolSync with Kopia for backups

**Networking:**
- Cilium CNI with eBPF datapath
- Gateway API for ingress
- Cloudflare Tunnel for external access
- k8s-gateway for internal DNS

**Security:**
- External Secrets Operator with 1Password
- SOPS with age encryption for Git-stored secrets
- cert-manager with Google Trust Services

## Development Guidelines

### Template Development

When working with Talos configuration templates:
- Templates are located in `cmd/homeops-cli/internal/templates/talos/`
- Use Jinja2 syntax with environment variable substitution
- Test template rendering with `make dev` before deployment
- 1Password references use format: `op://vault/item/field`

### Error Handling Patterns

The CLI uses structured error handling:
- Wrap errors with context using fmt.Errorf
- Use the common.ColorLogger for consistent output
- Implement retry logic for network operations
- Validate inputs early and provide clear error messages

### Testing

When adding new functionality:
- Add unit tests for internal packages
- Use the embedded test framework in `internal/testing/`
- Test CLI commands with dry-run flags where available
- Validate template rendering in isolation

### 1Password Integration

The CLI integrates with 1Password for secret management:
- Secrets are resolved during template rendering
- Bootstrap process validates 1Password connectivity
- Use 1Password references in YAML templates, not Go code
- Test authentication with preflight checks

## Data Restore Process

For restoring application data with VolSync and Longhorn v2, see the detailed guide:
[VolSync v2 Restore Process Documentation](./docs/volsync-v2-restore-process.md)

## Common Issues and Solutions

**Bootstrap Failures:**
- Ensure 1Password CLI is authenticated: `op signin`
- Verify all required tools are installed: `talosctl`, `kubectl`, `kustomize`, `op`, `helmfile`
- Check versions.env file exists and contains required versions
- Run with `--skip-preflight` only for debugging

**Template Rendering Issues:**
- Verify environment variables are set correctly
- Check template syntax with embedded validation
- Use dry-run mode to test template output
- Ensure 1Password references are accessible

**VM Deployment Problems:**
- Always use `--generate-iso` flag for custom Talos ISOs
- Verify TrueNAS credentials are configured
- Check ZVol naming conventions (no dashes in VM names)
- Ensure ISO is downloaded before VM creation

This CLI tool is designed for infrastructure automation and requires careful attention to the order of operations, especially during bootstrap and VM deployment processes.