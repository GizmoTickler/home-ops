# HomeOps CLI

`homeops-cli` is the Go-based operations tool for this repository. The built binary and the Cobra root command both use `homeops-cli`. It deploys Kubernetes (Flatcar/kubeadm, or legacy Talos) on Proxmox, TrueNAS, or vSphere.

## Build and Verify

```bash
cd cmd/homeops-cli
make build
make test
make check
```

## Configuration (homeops.yaml)

Everything environment-specific — cluster topology (node names/IPs, control-plane
VIP), hypervisor settings, state storage, and **where secrets come from** — lives
in a config file, not in the code. Nothing in the CLI is hardwired to 1Password
or to any particular network layout, so the tool is usable outside this repo.

```bash
homeops-cli config init            # scaffold a homeops.yaml (--backend env|op|file)
homeops-cli config show            # effective config + where it was loaded from
homeops-cli config doctor          # validate config, binaries, and secret resolution
homeops-cli config init --print-keys   # list every secret key and its default
```

Discovery order: `--config` flag → `$HOMEOPS_CONFIG` → `./homeops.yaml` →
`<git root>/homeops.yaml` → `~/.config/homeops/config.yaml`. With no file at
all, fully-portable defaults apply: every secret resolves from environment
variables and cluster state (kubeconfig, kubeadm PKI) is stored under
`~/.config/homeops/state`.

Secret references support several backends and can be mixed freely:

| Scheme | Source |
|---|---|
| `op://vault/item/field` | 1Password CLI (`op read`) |
| `env://VAR_NAME` | environment variable |
| `file:///path/to/file` | file contents (`~` expands) |
| `cmd://command args` | stdout of a command (pass, vault, sops…) |
| `literal://value` | the value itself (non-sensitive knobs) |

Embedded templates reference secrets by semantic key (`secret://talos_machine_ca_crt`),
which the `secrets:` map in homeops.yaml binds to a backend. Templates can also be
shadowed wholesale via `templates.dir`. This repo's own mapping is in the
repo-root [`homeops.yaml`](../../homeops.yaml) (1Password-backed).

## Core Commands

```bash
homeops-cli bootstrap            # defaults to the Flatcar/kubeadm provider
homeops-cli flatcar --help       # current provider (Flatcar Container Linux + kubeadm)
homeops-cli talos --help         # legacy provider (retained for reference/rollback)
homeops-cli k8s --help
homeops-cli volsync --help
homeops-cli workstation --help
homeops-cli config --help        # config scaffold / show / doctor
homeops-cli version
```

## Flatcar VM Workflows (current)

The cluster runs **Flatcar Container Linux + kubeadm**. `flatcar deploy-vm`
renders a Butane → Ignition config (injecting 1Password secrets), uploads it to
the Proxmox snippets store over SSH, and creates the VM; kubeadm init/join runs
on first boot and Cilium is then installed.

```bash
# Deploy the control-plane / all nodes on Proxmox
homeops-cli flatcar deploy-vm --nodes k8s-0,k8s-1,k8s-2 --concurrency 3

# Render just the Ignition or kubeadm config
homeops-cli flatcar render-ignition
homeops-cli flatcar gen-kubeadm
```

Kubernetes minor upgrades are GitOps-driven via the kubeadm System Upgrade
Controller Plan (`kubernetes/apps/system-upgrade/kubeadm-upgrade/`), not a CLI
command.

## Talos VM Workflows (legacy)

> **Legacy provider.** Retained for reference/rollback; the current cluster uses
> Flatcar + kubeadm (see above).

### Proxmox-first deployment

`talos deploy-vm` defaults to `proxmox`. If you run it with no flags, it opens the interactive flow and lets you choose provider, VM name, resource profile, and ISO behavior.

```bash
# Interactive deploy with Proxmox as the default provider
homeops-cli talos deploy-vm

# Single VM on Proxmox
homeops-cli talos deploy-vm --name test --generate-iso

# Batch deployment on Proxmox or vSphere
homeops-cli talos deploy-vm --name k8s --node-count 3 --concurrency 3 --generate-iso

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
homeops-cli talos manage-vm poweron --name k8s-0
homeops-cli talos manage-vm poweroff --name k8s-0
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
homeops-cli volsync snapshot --app paperless --namespace default
homeops-cli volsync restore --app paperless --namespace default
homeops-cli volsync snapshots --namespace default
homeops-cli volsync suspend --all -n default
homeops-cli volsync resume --all -n default
```

## Development Notes

- `make build` outputs `homeops-cli` in this directory.
- The CLI expects core environment such as `KUBECONFIG` and `KUBERNETES_VERSION`. `TALOSCONFIG` and `TALOS_VERSION` are needed only for the legacy Talos provider.
- Provider/hypervisor credentials resolve through the `secrets:` map in homeops.yaml (any backend), with plain environment variables as a final fallback. Run `homeops-cli config doctor` to see what resolves.
- `HOMEOPS_NO_INTERACTIVE=1` disables interactive prompts (CI mode).
- For Kubernetes GitOps changes, commit and push changes before reconciling Flux.
- Per-node VM hardware profiles (Proxmox VMIDs, MACs, storage pools, CPU pinning) remain code defaults in `internal/proxmox/vm_manager.go`, overridable per-deploy via flags; node names/IPs come from `cluster.nodes` in homeops.yaml.

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
