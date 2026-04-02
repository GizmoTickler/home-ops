# HomeOps CLI Command Reference

This is the canonical command reference for `homeops-cli`.

It is intentionally structured around the live Cobra command tree in `main.go` and `cmd/`, not around historical workflows. For the full flag surface of any command, use `homeops-cli <command> --help`.

## Command Tree

```text
homeops-cli
├── bootstrap
├── completion [bash|zsh|fish|powershell]
├── k8s
│   ├── browse-pvc
│   ├── node-shell
│   ├── sync-secrets
│   ├── prune-pods
│   ├── view-secret [secret-name]
│   ├── sync
│   ├── force-sync-externalsecret <name>
│   ├── upgrade-arc
│   ├── render-ks <ks.yaml>
│   ├── apply-ks [ks.yaml]
│   └── delete-ks <ks.yaml>
├── talos
│   ├── apply-node
│   ├── upgrade-node
│   ├── upgrade-k8s
│   ├── reboot-node
│   ├── shutdown-cluster
│   ├── reset-node
│   ├── reset-cluster
│   ├── kubeconfig
│   ├── prepare-iso
│   ├── deploy-vm
│   └── manage-vm
│       ├── list
│       ├── start
│       ├── stop
│       ├── poweron
│       ├── poweroff
│       ├── delete
│       ├── info
│       └── cleanup-zvols
├── volsync
│   ├── state
│   ├── suspend [name]
│   ├── resume [name]
│   ├── snapshot
│   ├── snapshot-all
│   ├── restore
│   ├── restore-all
│   └── snapshots
└── workstation
    ├── brew
    └── krew
```

## Root Usage

```bash
homeops-cli --help
homeops-cli --version
homeops-cli --log-level debug
```

If you run `homeops-cli` with no subcommand, it opens the interactive command menu.

## Bootstrap

Bootstraps the Talos cluster and cluster applications.

```bash
homeops-cli bootstrap
homeops-cli bootstrap --dry-run
homeops-cli bootstrap --skip-preflight --skip-crds
homeops-cli bootstrap --verbose
```

Key flags:

- `--root-dir`
- `--kubeconfig`
- `--talosconfig`
- `--k8s-version`
- `--talos-version`
- `--dry-run`
- `--skip-crds`
- `--skip-resources`
- `--skip-helmfile`
- `--skip-preflight`
- `--verbose`

## Talos

### Node and Cluster Operations

```bash
homeops-cli talos apply-node --ip 192.168.122.10
homeops-cli talos reboot-node --ip 192.168.122.10
homeops-cli talos upgrade-node --ip 192.168.122.10
homeops-cli talos upgrade-k8s
homeops-cli talos kubeconfig
homeops-cli talos shutdown-cluster
homeops-cli talos reset-node --ip 192.168.122.10
homeops-cli talos reset-cluster
```

### ISO Preparation

`prepare-iso` generates a Talos Factory ISO and uploads it to the selected provider. The provider default is `proxmox`.

```bash
homeops-cli talos prepare-iso
homeops-cli talos prepare-iso --provider proxmox
homeops-cli talos prepare-iso --provider vsphere
homeops-cli talos prepare-iso --provider truenas
```

### VM Deployment

`deploy-vm` defaults to `proxmox`. In interactive mode it prompts for provider, naming, batch settings, and resource profile.

```bash
# Interactive deployment
homeops-cli talos deploy-vm

# Single VM on default provider (proxmox)
homeops-cli talos deploy-vm --name test --generate-iso

# Batch deployment on Proxmox or vSphere
homeops-cli talos deploy-vm --name k8s --node-count 3 --concurrent 3 --generate-iso

# Batch naming with a non-zero start index
homeops-cli talos deploy-vm --name worker --node-count 2 --start-index 3 --generate-iso

# Explicit provider selection
homeops-cli talos deploy-vm --provider vsphere --name lab --node-count 3 --generate-iso
homeops-cli talos deploy-vm --provider truenas --name test --generate-iso

# Dry-run
homeops-cli talos deploy-vm --name test --dry-run
```

High-signal flags:

- `--provider` with `proxmox` default, or `vsphere` / `esxi` / `truenas`
- `--name`
- `--node-count`
- `--concurrent`
- `--start-index`
- `--memory`
- `--vcpus`
- `--disk-size`
- `--openebs-size`
- `--generate-iso`
- `--dry-run`
- `--datastore` and `--network` for vSphere
- `--pool`, `--skip-zvol-create`, and `--mac-address` for TrueNAS-specific flows

### VM Lifecycle Management

```bash
homeops-cli talos manage-vm list
homeops-cli talos manage-vm list --provider vsphere

homeops-cli talos manage-vm info --name k8s-0
homeops-cli talos manage-vm start --name k8s-0
homeops-cli talos manage-vm stop --name k8s-0
homeops-cli talos manage-vm poweron --name k8s-0
homeops-cli talos manage-vm poweroff --name k8s-0
homeops-cli talos manage-vm delete --name k8s-0 --force

homeops-cli talos manage-vm cleanup-zvols --vm-name old-node --force
```

Notes:

- `manage-vm` subcommands default to `proxmox`.
- `start`, `stop`, `poweron`, `poweroff`, `delete`, and `info` support interactive VM selection when `--name` is omitted.
- `cleanup-zvols` is TrueNAS-specific and requires `--vm-name`.

## Kubernetes

### PVC and Node Access

```bash
homeops-cli k8s browse-pvc --namespace default
homeops-cli k8s browse-pvc --namespace media --claim downloads

homeops-cli k8s node-shell
homeops-cli k8s node-shell --node k8s-0
```

Notes:

- `browse-pvc` installs and uses the `kubectl browse-pvc` plugin if needed.
- `node-shell` installs and uses the `kubectl node-shell` plugin if needed.

### Secret and ExternalSecret Operations

```bash
homeops-cli k8s sync-secrets
homeops-cli k8s sync-secrets --dry-run

homeops-cli k8s view-secret my-secret -n default
homeops-cli k8s view-secret my-secret -n default -k password
homeops-cli k8s view-secret my-secret -n default -o json
homeops-cli k8s view-secret

homeops-cli k8s force-sync-externalsecret my-secret -n default
homeops-cli k8s force-sync-externalsecret --all -n default
```

Notes:

- `view-secret` supports `table`, `json`, and `yaml` output.
- If you omit the secret name and `default` has no secrets, `view-secret` now prompts for another namespace instead of failing immediately.
- `force-sync-externalsecret` accepts either a secret name or `--all`.

### Pod and Flux Maintenance

```bash
homeops-cli k8s prune-pods
homeops-cli k8s prune-pods --phase Failed --namespace default
homeops-cli k8s prune-pods --dry-run

homeops-cli k8s sync --type helmrelease
homeops-cli k8s sync --type kustomization --namespace flux-system
homeops-cli k8s sync --type gitrepo --parallel

homeops-cli k8s upgrade-arc --force
```

Notes:

- `sync --type` accepts `gitrepo`, `helmrelease`, `kustomization`, or `ocirepository`.
- `upgrade-arc` uninstalls and reconciles ARC resources and asks for confirmation unless `--force` is set.

### Local Flux Kustomization Workflows

These commands work against Flux `ks.yaml` files and support multi-document files via `--name`.

```bash
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana -o rendered.yaml

homeops-cli k8s apply-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s apply-ks --dry-run

homeops-cli k8s delete-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s delete-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance --force
```

Notes:

- `apply-ks` supports interactive `ks.yaml` selection if no path is provided.
- `render-ks` and `delete-ks` require an explicit `ks.yaml` path.
- Use `--name` when one `ks.yaml` file contains multiple Flux `Kustomization` documents.

## VolSync

### Controller State

```bash
homeops-cli volsync state --action suspend
homeops-cli volsync state --action resume
```

### Resource Suspension and Resume

```bash
homeops-cli volsync suspend app-source -n default
homeops-cli volsync suspend --all -n default

homeops-cli volsync resume app-source -n default
homeops-cli volsync resume --all -n default
```

Notes:

- If `--namespace` is omitted, the CLI prompts for a namespace.
- `suspend` and `resume` target either a named `ReplicationSource` / `ReplicationDestination` or everything in a namespace via `--all`.

### Snapshots

```bash
homeops-cli volsync snapshot --namespace default --app paperless
homeops-cli volsync snapshot --namespace default --app paperless --wait=false

homeops-cli volsync snapshot-all
homeops-cli volsync snapshot-all --namespace default --dry-run
homeops-cli volsync snapshot-all --concurrency 5

homeops-cli volsync snapshots --namespace default
```

Notes:

- `snapshot` prompts for namespace and app when omitted.
- `snapshot-all` discovers `ReplicationSource` resources and can run in parallel.
- `snapshots` lists available snapshots for a namespace / application flow.

### Restore

```bash
homeops-cli volsync restore --namespace default --app paperless
homeops-cli volsync restore --namespace default --app paperless --previous 12
homeops-cli volsync restore --namespace default --app paperless --previous 12 --force

homeops-cli volsync restore-all --namespace default --force
```

Notes:

- `restore` prompts for namespace, application, and snapshot when omitted.
- `restore-all` is the bulk restore workflow for a namespace or broader recovery operation; use `--help` before running it on live workloads.

## Workstation

```bash
homeops-cli workstation brew
homeops-cli workstation krew
```

These install or validate local workstation dependencies used by the rest of the CLI.

## Completion

```bash
homeops-cli completion bash
homeops-cli completion zsh
homeops-cli completion fish
homeops-cli completion powershell
```

For shell-specific setup, see [`COMPLETION.md`](./COMPLETION.md).

## Practical Workflows

### Prepare and Deploy Talos VMs on Proxmox

```bash
homeops-cli talos prepare-iso
homeops-cli talos deploy-vm --name k8s --node-count 3 --concurrent 3 --generate-iso
```

### Inspect a Secret Interactively

```bash
homeops-cli k8s view-secret
```

### Render and Apply a Local Flux Kustomization

```bash
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s apply-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
```

### Snapshot and Restore an Application

```bash
homeops-cli volsync snapshot --namespace default --app paperless
homeops-cli volsync restore --namespace default --app paperless --previous 12
```

## Maintenance Notes

- This file is the single source of truth for command inventory.
- If a command changes in `cmd/`, update this file in the same change.
- Prefer documenting stable command shapes and high-signal flags here. Let `--help` carry the full flag surface.
