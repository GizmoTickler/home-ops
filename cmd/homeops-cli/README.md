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

Node inventory lives under `cluster.nodes`. It is the shared source for node
names/IPs and per-node VM hardware overrides used by Flatcar deployment and
provider-agnostic VM helpers; the full schema is emitted by `config init`.

```yaml
cluster:
  nodes:
    - name: k8s-0
      ip: 192.168.122.10
      vm:
        vmid: 200
        mac: "00:a0:98:00:00:01"
        boot_storage: nvme-mirror
```

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
homeops-cli workstation --help   # OS-aware tool setup (see workstation setup)
homeops-cli vm --help            # provider-agnostic VM platform (see below)
homeops-cli op --help            # 1Password item management
homeops-cli config --help        # config scaffold / show / doctor
homeops-cli version
```

## 1Password Inventory (`homeops-cli op`)

The `op` group manages 1Password item metadata and can audit Kubernetes
ExternalSecret references against the accessible item inventory.

```bash
homeops-cli op audit --vault all
homeops-cli op audit --vault Infrastructure --output json
```

## VM Platform (`homeops-cli vm`)

Provider-first VM management — any OS, not just k8s nodes. Pick the
hypervisor, then the verb: `vm proxmox|truenas|vsphere <verb>`. The same
verbs also work directly under `vm` as hidden shorthands acting on
`hypervisors.default` (with `--provider` to override).

```bash
# Create general-purpose VMs from cloud images (latest stable resolved automatically)
homeops-cli vm proxmox create --name dev-vm --os ubuntu
homeops-cli vm proxmox create --name rocky0 --os rocky --memory 8192 --ip 192.168.120.50/22 --gateway 192.168.123.254
homeops-cli vm proxmox create --name rhel0 --os rhel            # set images.rhel in homeops.yaml first
homeops-cli vm truenas create --name dev0 --os ubuntu           # NoCloud seed ISO over SSH
homeops-cli vm vsphere create --name dev-vm --template ubuntu-tpl  # template clone + guestinfo

# Reusable templates
homeops-cli vm proxmox template import --name ubuntu-tpl --os ubuntu  # image + template flag
homeops-cli vm vsphere template import --from-vm golden               # convert an existing VM

# Day-2 lifecycle
homeops-cli vm proxmox list / start / stop / info / delete / restart
homeops-cli vm proxmox set --name dev-vm --memory 16384 --cores 8
homeops-cli vm truenas resize-disk --name dev0 --disk openebs --grow 100G
homeops-cli vm proxmox snapshot create|list|rollback|delete --name dev-vm --snap pre-upgrade
homeops-cli vm proxmox clone --name template --to dev-vm2
homeops-cli vm proxmox ip dev-vm                                # guest agent / VMware Tools / cluster config
homeops-cli vm proxmox ssh dev-vm --user ubuntu
homeops-cli vm truenas console dev0                             # noVNC+xterm.js / SPICE / WebMKS URL
homeops-cli vm list                                             # shorthand: hypervisors.default
homeops-cli vm list --all-providers                             # merge proxmox + truenas + vsphere (errors shown as notes)
```

The full matrix (create, set, resize-disk, restart, snapshot CRUD, clone, ip,
ssh, console, plus list/start/stop/info/delete) works on all three
hypervisors. Where a platform genuinely lacks a capability the command says
so uniformly — `not supported on <provider>: <reason>` — never a silent
no-op. The known gaps: TrueNAS cannot report guest IPs (no guest agent; the
`ip`/`ssh` commands fall back to `cluster.nodes`), TrueNAS has no template
concept, vSphere imports templates only from an existing VM (`--from-vm`),
and vSphere clones are always full (no `--linked`/`--vmid`).

Provider-specific config (homeops.yaml): `hypervisors.truenas.image_dir`
(where cloud images and seed ISOs are staged on the NAS; defaults to an
`images` dir next to `iso_dir`), `hypervisors.vsphere.template` (default
template for `vm create --provider vsphere`). TrueNAS staging SSH uses
`secrets.truenas_username` (default: `truenas_admin`).

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
homeops-cli flatcar render-ignition --output-file ./k8s-0.ign
homeops-cli flatcar gen-kubeadm

# Node lifecycle (reboot-node/reset-node prompt for the node if --node is omitted)
homeops-cli flatcar reboot-node --node k8s-1
homeops-cli flatcar reset-node --node k8s-1 --force      # kubeadm reset (destructive)
homeops-cli flatcar save-pki                             # capture live cluster PKI into the configured store
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

VM lifecycle (list/info/delete/poweron/poweroff/…) is handled by the
provider-agnostic **`vm` platform** (see the "VM Platform" section above) —
`homeops-cli vm proxmox|truenas|vsphere <verb>`. The old `talos manage-vm`
subcommand is deprecated (hidden) and forwards to `vm`; use `vm` directly:

```bash
homeops-cli vm proxmox list
homeops-cli vm proxmox info --name k8s-0
homeops-cli vm proxmox delete --name test --force
homeops-cli vm proxmox poweron --name k8s-0
homeops-cli vm proxmox poweroff --name k8s-0
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
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana --output-file ./rendered.yaml
homeops-cli k8s apply-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s delete-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
```

### Cluster triage and maintenance windows

`k8s doctor` is read-only and reports Flux, node, pod, scale-csi, and certificate
health in table or JSON form. Pending pods younger than `--pending-grace` are
ignored so short-lived restore/mover pods do not fail the check.

`k8s storage-report` rolls up PVC/PV capacity by StorageClass and snapshots by
class, reports scale-csi controller/node readiness and metrics when reachable,
and retains the orphaned-PVC, unhealthy-PV, and VolSync coverage checks.

```bash
homeops-cli k8s doctor
homeops-cli k8s doctor --pending-grace 20m --output json
homeops-cli k8s suspend radarr --namespace downloads --dry-run
homeops-cli k8s resume radarr --namespace downloads
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
homeops-cli volsync migrate paperless --namespace default --yes
homeops-cli volsync snapshots --app paperless
homeops-cli volsync status --stale-after 36h --output json
homeops-cli volsync suspend --all -n default
homeops-cli volsync resume --all -n default
```

`volsync migrate` performs the guarded VolSync storage cutover and defaults to
the `scale-nvmeof` StorageClass and `scale-snapshot` VolumeSnapshotClass. It
requires those values to be present in the app namespace's Flux Kustomization
before it takes a fresh backup. `volsync status` and `volsync state` show PVC
StorageClasses, while successful restores report the spent snapshot and
intermediate PVC that scale-csi garbage collection removes after 24 hours.

## Quality Gates

Every change must pass the full gate (CI runs the same steps via
`.github/workflows/homeops-cli.yaml` on anything touching `cmd/homeops-cli`):

```bash
make check        # fmt + vet + golangci-lint (.golangci.yml) + tests
```

Scripting/automation: list commands emit machine-readable output —
`vm list --output json|yaml`, `op list --output json|yaml`, `op vaults list
--output json|yaml`, `volsync snapshots --format json|yaml`. All tables degrade to
plain aligned columns when piped (no ANSI), and prompts are disabled with
`HOMEOPS_NO_INTERACTIVE=1` (pass `--yes`/`--all` style flags in CI).

## Development Notes

- `make build` outputs `homeops-cli` in this directory.
- The CLI expects core environment such as `KUBECONFIG` and `KUBERNETES_VERSION`. `TALOSCONFIG` and `TALOS_VERSION` are needed only for the legacy Talos provider.
- Provider/hypervisor credentials resolve through the `secrets:` map in homeops.yaml (any backend), with plain environment variables as a final fallback. Run `homeops-cli config doctor` to see what resolves.
- `HOMEOPS_NO_INTERACTIVE=1` disables interactive prompts (CI mode).
- For Kubernetes GitOps changes, commit and push changes before reconciling Flux.
- Per-node VM hardware profiles (Proxmox VMIDs, MACs, storage pools, CPU pinning) ship as embedded defaults in `internal/config` and are overridable per node via `cluster.nodes[].vm` (and `cluster.nodes[].vm.providers.talos`/`.flatcar` overlays) in homeops.yaml; node names/IPs come from the same `cluster.nodes`.

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
