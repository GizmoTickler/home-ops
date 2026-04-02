# HomeOps CLI

`homeops-cli` is the Go-based operations tool for this repository. The built binary and the Cobra root command both use `homeops-cli`.

## Build and Verify

```bash
cd cmd/homeops-cli
make build
make test
make check
```

## Core Commands

```bash
homeops-cli bootstrap
homeops-cli talos --help
homeops-cli k8s --help
homeops-cli volsync --help
homeops-cli workstation --help
```

## Talos VM Workflows

### Proxmox-first deployment

`talos deploy-vm` now defaults to `proxmox`. If you run it with no flags, it opens the interactive flow and lets you choose provider, VM name, resource profile, and ISO behavior.

```bash
# Interactive deploy with Proxmox as the default provider
homeops-cli talos deploy-vm

# Single VM on Proxmox
homeops-cli talos deploy-vm --name test --generate-iso

# Batch deployment on Proxmox or vSphere
homeops-cli talos deploy-vm --name k8s --node-count 3 --concurrent 3 --generate-iso

# Start numbering at a higher index
homeops-cli talos deploy-vm --name worker --node-count 2 --start-index 3 --generate-iso

# Explicit provider override
homeops-cli talos deploy-vm --provider vsphere --name lab --node-count 3 --generate-iso
homeops-cli talos deploy-vm --provider truenas --name test --generate-iso
```

### ISO preparation

Use `prepare-iso` when you want to upload a reusable Talos ISO before deployment instead of generating it during `deploy-vm`.

```bash
homeops-cli talos prepare-iso
homeops-cli talos prepare-iso --provider vsphere
homeops-cli talos prepare-iso --provider truenas
```

### VM lifecycle management

```bash
homeops-cli talos manage-vm list
homeops-cli talos manage-vm info --name k8s-0
homeops-cli talos manage-vm delete --name test --force
homeops-cli talos poweron --name k8s-0
homeops-cli talos poweroff --name k8s-0
```

## Kubernetes Operations

### Secrets

`k8s view-secret` supports direct lookup and interactive selection. If you omit the secret name and the `default` namespace has no secrets, the CLI now prompts you to pick another namespace instead of failing immediately.

```bash
# View a known secret
homeops-cli k8s view-secret my-secret -n default

# Print a single key
homeops-cli k8s view-secret my-secret -n default -k password

# Interactive secret selection with namespace fallback
homeops-cli k8s view-secret
```

### Flux-local Kustomization workflows

The Kustomization helpers work with `ks.yaml` files and support multi-document files via `--name`.

```bash
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana
homeops-cli k8s apply-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s delete-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
```

### Other common operations

```bash
homeops-cli k8s browse-pvc --namespace default
homeops-cli k8s prune-pods --dry-run
homeops-cli k8s sync --type helmrelease
homeops-cli k8s force-sync-externalsecret my-secret -n default
```

## VolSync Workflows

```bash
homeops-cli volsync snapshot --pvc data-pvc --namespace default
homeops-cli volsync restore --pvc data-pvc --namespace default
homeops-cli volsync snapshots --namespace default
homeops-cli volsync suspend --all -n default
homeops-cli volsync resume --all -n default
```

## Development Notes

- `make build` outputs `homeops-cli` in this directory.
- The CLI expects core environment such as `KUBECONFIG`, `TALOSCONFIG`, `KUBERNETES_VERSION`, and `TALOS_VERSION`.
- For Talos/vSphere/TrueNAS credentials, the code prefers 1Password and falls back to environment variables.
- For Kubernetes GitOps changes, commit and push changes before reconciling Flux.

## Pre-commit

This repo now includes a repo-root `.pre-commit-config.yaml` with a local secret scanning hook.

```bash
pre-commit install
pre-commit run --all-files
```

The hook is designed to block high-signal secrets such as private keys and common live token formats before they are committed.

## Additional Docs

- [Testing Guide](./docs/TESTING.md)
- [Code Review Notes](./docs/CODE_REVIEW.md)
- [Coverage Review](./docs/COVERAGE_REVIEW.md)
