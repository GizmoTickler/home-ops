# HomeOps CLI Command Reference

This is the canonical command reference for `homeops-cli`.

It is intentionally structured around the live Cobra command tree in `main.go` and `cmd/`, not around historical workflows. For the full flag surface of any command, use `homeops-cli <command> --help`.

## Command Tree

```text
homeops-cli
├── bootstrap
├── completion [bash|zsh|fish|powershell]
├── flatcar                  # current provider (Flatcar Container Linux + kubeadm)
│   ├── render-ignition
│   ├── gen-kubeadm
│   ├── deploy-vm
│   ├── save-pki
│   ├── kubeconfig
│   ├── os-status
│   ├── reset-node
│   └── reset-cluster
├── k8s
│   ├── browse-pvc
│   ├── node-shell
│   ├── sync-secrets
│   ├── prune-pods
│   ├── view-secret [secret-name]
│   ├── sync
│   ├── force-sync-externalsecret <name>
│   ├── upgrade-arc
│   ├── render-ks [ks.yaml]
│   ├── diff [ks.yaml]
│   ├── apply-ks [ks.yaml]
│   ├── delete-ks <ks.yaml>
│   ├── doctor
│   ├── net-doctor
│   ├── storage-report
│   ├── right-size
│   ├── flux-tree [kustomization-name]
│   ├── upgrade-status
│   ├── support-bundle
│   ├── etcd
│   │   ├── backup
│   │   ├── status
│   │   └── restore <snapshot-file>
│   ├── certs
│   └── node
│       └── maintenance [enter|exit] <node>
├── talos                    # legacy provider (retained for reference/rollback)
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
├── vm                       # VM platform, provider-first
│   ├── proxmox|truenas|vsphere
│   │   ├── create
│   │   ├── template
│   │   │   └── import
│   │   ├── clone
│   │   ├── snapshot [create|list|rollback|delete]
│   │   ├── ip [name]
│   │   ├── ssh [name]
│   │   ├── console [name]
│   │   ├── set / resize-disk / restart
│   │   ├── list / start / stop / poweron / poweroff / delete / info
│   │   └── cleanup-zvols              # truenas only
│   └── <verb>                         # hidden shorthand: hypervisors.default
├── op                       # 1Password item management
│   ├── list / get / reveal / create / edit / delete
│   ├── vaults list
│   ├── move <item>
│   └── duplicate <item>
├── config
│   ├── init
│   ├── show
│   └── doctor [--network]
├── volsync
│   ├── state [suspend|resume]
│   ├── suspend [name]
│   ├── resume [name]
│   ├── snapshot
│   ├── snapshot-all
│   ├── restore
│   ├── restore-all
│   ├── verify --app <name>
│   └── snapshots
├── workstation
│   ├── setup [--all] [--upgrade] [--dry-run]
│   ├── brew
│   └── krew
├── self-update
└── version
```

## Root Usage

```bash
homeops-cli --help
homeops-cli --version
homeops-cli --log-level debug
homeops-cli self-update --check
```

If you run `homeops-cli` with no subcommand, it opens the interactive command menu.

### Self-update

```bash
# Query the latest release without changing the current binary
homeops-cli self-update --check
homeops-cli self-update --check --output json

# Download the matching darwin/linux amd64/arm64 release, verify its SHA256,
# and atomically replace the current executable
homeops-cli self-update --yes
```

`self-update` reads the latest `GizmoTickler/home-ops` GitHub release and uses
`GITHUB_TOKEN` when set. It accepts only HTTPS release/download URLs, verifies
the selected `.tar.gz` against `checksums.txt`, and never runs the downloaded
binary. Development builds refuse self-update unless `--force` is supplied.

## Bootstrap

Bootstraps the cluster and cluster applications. Defaults to the Flatcar
Container Linux + kubeadm provider (`--provider flatcar`); pass
`--provider talos` for the legacy Talos path.

```bash
homeops-cli bootstrap                       # Flatcar/kubeadm (default)
homeops-cli bootstrap --plan                # complete ordered plan; no infrastructure changes
homeops-cli bootstrap --plan --check-secrets --output json
homeops-cli bootstrap --dry-run
homeops-cli bootstrap --skip-preflight --skip-crds
homeops-cli bootstrap --verbose
homeops-cli bootstrap --skip-kubeadm        # Flatcar: post-CNI bootstrap only
homeops-cli bootstrap --provider talos      # legacy Talos path
```

Key flags:

- `--provider` (`flatcar` default, or `talos`)
- `--plan` (pure config/template introspection; prints the complete ordered plan and exits)
- `--check-secrets` (with `--plan`, availability-check listed references without printing references or values)
- `--output` (`table` or `json`, with `--plan`)
- `--root-dir`
- `--kubeconfig`
- `--k8s-version`
- `--skip-kubeadm` (Flatcar: skip kubeadm init/join; run only post-CNI bootstrap)
- `--fresh-pki` (Flatcar: mint a NEW cluster CA instead of restoring the persisted PKI from 1Password; breaks existing kubeconfigs)
- `--talosconfig` (legacy Talos provider only)
- `--talos-version` (legacy Talos provider only)
- `--dry-run`
- `--skip-crds`
- `--skip-resources`
- `--skip-helmfile`
- `--skip-preflight`
- `--verbose`

## Flatcar

Manages Flatcar Container Linux nodes (the **current** cluster provider, using
kubeadm). For the full flag surface use `homeops-cli flatcar <command> --help`.

```bash
# Render a node's Butane → Ignition config (1Password secrets injected)
homeops-cli flatcar render-ignition

# Generate the kubeadm init/join config
homeops-cli flatcar gen-kubeadm

# Deploy Flatcar k8s VM(s). --provider selects the hypervisor (default proxmox);
# kubeadm init/join runs on first boot once the node reads its Ignition.
# Proxmox (default): boot a pre-staged image; Ignition uploaded to the PVE
# snippets store over SSH and attached via fw_cfg.
homeops-cli flatcar deploy-vm --nodes k8s-0,k8s-1,k8s-2 --concurrency 3 \
  --image-volume nvme1:vm-200-disk-0

# vSphere/ESXi: clone a pre-imported Flatcar OVA template; Ignition via guestinfo.
homeops-cli flatcar deploy-vm --provider vsphere --nodes k8s-0 \
  --vsphere-template flatcar-prod --datastore local-nvme1 --vsphere-network vl999

# TrueNAS SCALE: boot a pre-staged image zvol; Ignition via qemu fw_cfg
# (command_line_args), staged to a dataset on the NAS over SSH.
homeops-cli flatcar deploy-vm --provider truenas --nodes k8s-0 \
  --truenas-pool flashstor --network-bridge br0

# Capture the live cluster PKI (CA/SA/front-proxy/etcd CA) into
# op://Infrastructure/kubernetes-pki so bootstrap can restore it for a STABLE
# cluster identity across rebuilds. Run after the cluster is up + after any CA
# rotation. `bootstrap` restores it by default (opt out with --fresh-pki).
homeops-cli flatcar save-pki                # reads from k8s-0 by default
homeops-cli flatcar save-pki --node k8s-1

# Fetch the cluster kubeconfig (admin.conf) from a node, point the server at the
# VIP, write it locally; --push also stores it in 1Password, --pull retrieves it.
homeops-cli flatcar kubeconfig                       # -> $KUBECONFIG or ~/.kube/config
homeops-cli flatcar kubeconfig --push                # also save to op://Infrastructure/kubeconfig
homeops-cli flatcar kubeconfig --pull --output ./kc  # retrieve from 1Password

# Read Flatcar version, update-engine, reboot, kernel, and boot-time state over SSH
homeops-cli flatcar os-status
homeops-cli flatcar os-status --output json
```

`os-status` is read-only. It checks every `cluster.nodes` entry, warns when
Flatcar versions differ or a reboot is pending, and exits nonzero only when a
node cannot be inspected over SSH. `update_engine_client -status` is attempted
without elevation first and retried with non-interactive `sudo` when required.

`deploy-vm` flags. `--provider` selects the hypervisor — `proxmox` (default),
`vsphere` (alias `esxi`), or `truenas`. Common: `--nodes`, `--concurrency`,
`--vip`, `--kube-vip-version`, `--pause-image`, `--interface`, `--power-on`,
`--dry-run`. Per-hypervisor (the Ignition transport differs):

- **proxmox** — `--image-path` (import-from) or `--image-volume` (existing
  volume), `--snippets-dir`, `--pve-ssh-host`/`--pve-ssh-user`/`--pve-ssh-port`.
  Ignition is written to the PVE snippets dir and attached via **fw_cfg**.
- **vsphere** (alias **esxi**) — `--vsphere-template` (the imported Flatcar OVA),
  `--datastore`, `--vsphere-network`, `--vcpus`/`--memory` (0 = inherit template).
  Ignition is delivered via VMware **guestinfo** (base64); no SSH upload.
- **truenas** — `--truenas-pool`, `--network-bridge`, `--boot-zvol` (single-node
  override; otherwise `<pool>/VM/<node>-boot`), `--ignition-dir` (default
  `/mnt/<pool>/VM`), `--truenas-ssh-host`/`--truenas-ssh-user`, `--truenas-port`.
  Ignition is staged to a dataset on the NAS and attached via qemu **fw_cfg**
  (`command_line_args`).

### Flatcar lifecycle vs the legacy Talos verbs

Some operations the `talos` group exposes as imperative verbs are handled
differently on Flatcar/kubeadm and intentionally have **no `flatcar` verb**:

- **Kubernetes upgrades** — GitOps-driven by the System Upgrade Controller Plan
  (`kubernetes/apps/system-upgrade/kubeadm-upgrade/`); bump the Plan version in
  Git rather than running an imperative `upgrade-k8s`. (Node-level changes ship
  via Ignition/sysext.)
- **Node reboots** — orchestrated by `kured` (drains + reboots one node at a time
  when a sysext/OS update stages `/run/reboot-required`); no manual `reboot-node`.
- **VM lifecycle** (list/start/stop/poweron/poweroff/info/delete) — use
  `homeops-cli talos manage-vm …`, which drives the **shared, provider-agnostic**
  Proxmox VM manager by VM name and therefore manages the `k8s-0/1/2` Flatcar VMs
  too. (The command lives under `talos` for historical reasons; the underlying
  manager is not Talos-specific.)

The `flatcar` verbs that *do* exist — `bootstrap --provider flatcar`, `deploy-vm`,
`render-ignition`, `gen-kubeadm`, `save-pki`, `kubeconfig`, `reset-node`,
`reset-cluster` — cover the operations without a GitOps/shared-tool equivalent.

## Talos (legacy)

> **Legacy provider.** The cluster runs Flatcar + kubeadm (see **Flatcar**
> above). The `talos` command group is retained for reference/rollback; the
> commands below operate on Talos nodes only.

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
homeops-cli talos deploy-vm --name k8s --node-count 3 --concurrency 3 --generate-iso

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
- `--concurrency`
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

## VM Platform (`vm`)

Provider-first VM management: `vm proxmox|truenas|vsphere <verb>`. The same
verbs also work directly under `vm` (hidden shorthand) with
`--provider` / `hypervisors.default` selecting the hypervisor. VM names
complete live from the hypervisor.

```bash
# Create from a cloud image (ubuntu/rocky/debian/fedora resolve automatically)
homeops-cli vm proxmox create --name dev-vm --os ubuntu
homeops-cli vm truenas create --name dev0 --os rocky --ip 192.168.120.50/22 --gateway 192.168.123.254
homeops-cli vm vsphere create --name dev-vm --template ubuntu-tpl

# Templates
homeops-cli vm proxmox template import --name ubuntu-tpl --os ubuntu
homeops-cli vm vsphere template import --from-vm golden   # convert existing VM

# Day-2 operations
homeops-cli vm proxmox set --name dev-vm --memory 16384 --cores 8
homeops-cli vm proxmox resize-disk --name dev-vm --grow 20G
homeops-cli vm truenas snapshot create --name dev0 --snap pre-upgrade
homeops-cli vm proxmox clone --name dev-vm --to dev-vm2
homeops-cli vm proxmox ip dev-vm
homeops-cli vm proxmox ssh dev-vm --user ubuntu
homeops-cli vm truenas console dev0
homeops-cli vm proxmox list / start / stop / restart / info / delete

# Shorthand against hypervisors.default (hidden from help, fully supported)
homeops-cli vm list
homeops-cli vm ssh dev-vm
```

Feature × provider matrix:

| Feature | Proxmox | TrueNAS | vSphere |
|---|---|---|---|
| create | cloud image + cloud-init drive | cloud image → zvol + NoCloud seed ISO | template clone + guestinfo |
| template import | image import + template flag; `--from-vm` | not supported (no template concept) | `--from-vm` only (qcow2 needs VMDK/OVA) |
| set / resize-disk / restart | ✓ | ✓ | ✓ |
| snapshot create/list/rollback/delete | ✓ (native) | ✓ (ZFS, all zvols under one name) | ✓ (native, tree listing) |
| clone | full or `--linked`, `--vmid` | ZFS clone (always linked) | full only |
| ip | ✓ (guest agent) | not supported (no guest agent; falls back to cluster.nodes) | ✓ (VMware Tools) |
| ssh | ✓ | ✓ (via cluster.nodes fallback) | ✓ |
| console | noVNC + xterm.js URLs | SPICE web / native URL | WebMKS ticket URL |
| list/start/stop/info/delete | ✓ | ✓ | ✓ |

Unsupported cells fail loudly and uniformly: `not supported on <provider>: <reason>`.

## 1Password (`op`)

```bash
homeops-cli op list --vault Infrastructure
homeops-cli op get my-item --field API_TOKEN --reveal
homeops-cli op reveal my-item [field]             # clear text without flags (menu-friendly)
homeops-cli op create my-svc --vault Infrastructure --field API_TOKEN=...   # values via stdin template
homeops-cli op edit my-svc --field API_HOST=10.0.0.5
homeops-cli op delete old-item --archive
homeops-cli op vaults list
homeops-cli op move my-svc --vault Private --to-vault Infrastructure
homeops-cli op duplicate prod-creds --to-vault Staging --name staging-creds
```

## Config

```bash
homeops-cli config init [--backend env|op|file]   # scaffold homeops.yaml
homeops-cli config show                            # effective config (no secret values)
homeops-cli config doctor                          # offline validation
homeops-cli config doctor --network                # + hypervisor API probes, image URL HEAD checks
```

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
homeops-cli k8s view-secret my-secret -n default -k password --unsafe-reveal-values --i-understand-this-prints-secrets

homeops-cli k8s force-sync-externalsecret my-secret -n default
homeops-cli k8s force-sync-externalsecret --all -n default
```

Notes:

- `view-secret` supports `table`, `json`, and `yaml` output and shows key metadata by default: decoded byte length and a short SHA-256 fingerprint prefix.
- Decoded secret values are only printed when both `--unsafe-reveal-values` and `--i-understand-this-prints-secrets` are provided. Redirected or piped unsafe output also requires `--unsafe-force-non-tty`.
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

### Node Maintenance

```bash
# Print the complete plan, confirm, set Ceph noout, cordon, and drain
homeops-cli k8s node maintenance enter k8s-0

# Also stop the VM selected by hypervisors.default and the node's configured VMID
homeops-cli k8s node maintenance enter k8s-1 --shutdown-vm --drain-timeout 10m --yes

# Start the VM, wait for kubelet Ready, uncordon, restore Ceph, and report health
homeops-cli k8s node maintenance exit k8s-1 --start-vm --timeout 15m
homeops-cli k8s node maintenance exit k8s-1 --output json
```

The workflow only accepts nodes listed under `cluster.nodes`. Enter requires a
Ready node, records whether it owns the Ceph `noout` flag, and rolls back a
new cordon and maintenance-owned `noout` when a later step fails. A pre-existing
cordon or `noout` is preserved. If the `rook-ceph` namespace is absent, Ceph
steps are reported as skipped. Global `--yes` bypasses confirmation.

`cluster.maintenance.drain_timeout` (default `5m`) and
`cluster.maintenance.timeout` (default `10m`) provide lazy configuration
defaults; explicit flags take precedence.

### Kubeadm Disaster Recovery and PKI

```bash
# Create an in-pod etcd snapshot, verify it, download it, and retain 7 by default
homeops-cli k8s etcd backup
homeops-cli k8s etcd backup --output /secure/backups/etcd --keep 14

# Check etcd membership, endpoint health, and local backup freshness
homeops-cli k8s etcd status
homeops-cli k8s etcd status --stale-after 24h --output json

# Safe default: render the complete restore plan and make no changes
homeops-cli k8s etcd restore /secure/backups/etcd/etcd-snapshot-k8s-0-20260714T123456Z.db

# Execute only after checksum/status/SSH/revision preflight and typing cluster.name
homeops-cli k8s etcd restore /secure/backups/etcd/etcd-snapshot-k8s-0-20260714T123456Z.db --execute

# Automation must provide both --execute and global --yes
homeops-cli k8s etcd restore /secure/backups/etcd/etcd-snapshot-k8s-0-20260714T123456Z.db --execute --yes

# Check kubeadm PKI on every cluster.nodes control-plane node over SSH
homeops-cli k8s certs
homeops-cli k8s certs --warn-days 45 --fail-on-warn --output json

# Destructive paths are confirmation-gated; global --yes is suitable for CI
homeops-cli k8s certs --renew
homeops-cli k8s certs --renew --restart-control-plane --yes
```

`state.etcd_backup.dir` controls the default local snapshot directory and
`state.etcd_backup.keep` controls retention. `--output`/`--keep` override those
values lazily after the selected `homeops.yaml` has loaded. Restore requires the
snapshot's adjacent `.sha256` file and validates it with `etcdutl` locally or in
a local container. It verifies SSH and reads the etcd image from every configured
`cluster.nodes` member before mutation. During execution it parks etcd and
kube-apiserver static-pod manifests, preserves every old `member` directory, and
restores with the parked image through Flatcar's containerd `ctr`. A partial
failure intentionally leaves the cluster parked and prints the exact holding,
snapshot, and preserved-member paths; it never attempts an automatic rollback.

Certificate renewal
does not make new certificates active until the kube-apiserver,
kube-controller-manager, kube-scheduler, and etcd static pods restart on every
control-plane node. `--restart-control-plane` performs that restart one
component at a time and requires `--renew`.

### Read-only Cluster Triage

```bash
# Check Flux, node, pod, Ceph, and certificate health
homeops-cli k8s doctor
homeops-cli k8s doctor --output json

# Trace Gateway API parents/backends, Envoy Gateways, cloudflared, DNS, and TLS
homeops-cli k8s net-doctor
homeops-cli k8s net-doctor --resolve home.example.com --resolve status.example.com
homeops-cli k8s net-doctor --probe --probe-timeout 5s
homeops-cli k8s net-doctor --output json

# Audit orphaned claims, unhealthy PVs, Ceph capacity, overcommit, and backup gaps
homeops-cli k8s storage-report
homeops-cli k8s storage-report --namespace media --ceph-warn-percent 75
homeops-cli k8s storage-report --output json --fail-on-findings

# Discover Flux Kustomizations, then trace a dependency tree and its root blocker
homeops-cli k8s flux-tree
homeops-cli k8s flux-tree radarr
homeops-cli k8s flux-tree radarr --all --output json

# Compare SUC plans and jobs with apiserver/kubelet/containerd/OS versions
homeops-cli k8s upgrade-status
homeops-cli k8s upgrade-status --output json

# Find workload containers whose requests are too high or too low
homeops-cli k8s right-size
homeops-cli k8s right-size --namespace media --window 14d
homeops-cli k8s right-size --min-savings 25 --output json

# Create a redaction-checked diagnostic archive; skip SSH probes when needed
homeops-cli k8s support-bundle
homeops-cli k8s support-bundle --no-ssh
homeops-cli k8s support-bundle --output ./cluster-diagnostics.tar.gz
```

These commands only issue read-only Kubernetes API queries. `net-doctor` also
performs local DNS lookups against a discovered DNS-controller Service address
when `--resolve` is supplied. With `--probe`, it concurrently sends HTTPS GETs
for every HTTPRoute hostname directly to the matching Gateway Service address,
using the route hostname for TLS SNI and the HTTP Host header; public route DNS
is not used. The `PROBES` group records TCP/TLS/chain/expiry/HTTP/latency results.
Without `--resolve` or `--probe`, it performs no active network probe and its
output is unchanged. Both `doctor` and `net-doctor` exit 1 when a `FAIL` check
is present.
`storage-report`
returns zero when it finds hygiene issues unless `--fail-on-findings` is set.
`flux-tree` defaults to the `flux-system` namespace, includes unhealthy nested
HelmReleases, and uses `--all` to include ready HelmReleases too.
`upgrade-status` reads all `plans.upgrade.cattle.io`, reports active/failed SUC
Jobs and their pod-derived reasons, marks nodes `UpToDate`, `Pending`, or
`Unknown`, shows each Plan's `status.latestHash` as its last-applied plan hash,
and warns when apiserver/kubelet skew exceeds one minor. It exits
nonzero only when an SUC upgrade Job has failed (apart from Kubernetes API or
input errors that prevent a report).

`right-size` discovers a `vmsingle*` or `vmselect*` Service in
`cluster.observability.namespace` (default `observability`), opens a temporary
loopback `kubectl port-forward`, and compares historical CPU p95 and memory
p95/max usage with requests and limits. If VictoriaMetrics cannot be queried,
it prints a prominent warning and falls back to point-in-time
`metrics.k8s.io` data. `--min-savings` is a percentage filter for small
overprovisioning findings. It is advisory and always exits zero after a report
is produced.

`support-bundle` collects the existing doctor, network, storage, Flux, etcd,
certificate, Flatcar OS, upgrade, event, node, and version reports into a
timestamped `.tar.gz`. Collectors are isolated: a failed probe becomes an
`.error` entry and does not stop the archive. `--no-ssh` skips certificate and
Flatcar OS probes. Before publishing the archive, the command scans every
entry for private keys, kubeconfig key data, common token/password shapes, and
other likely secret material; a match refuses the entire bundle. The manifest
records contents, component versions, timings, and per-collector status.

### Local Flux Kustomization Workflows

These commands work against Flux `ks.yaml` files and support multi-document files via `--name`.

```bash
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana --output-file rendered.yaml

homeops-cli k8s diff ./kubernetes/apps/observability/grafana/ks.yaml --name grafana
homeops-cli k8s diff ./kubernetes/apps/observability/grafana/ks.yaml --name grafana --output json

homeops-cli k8s apply-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s apply-ks --dry-run

homeops-cli k8s delete-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s delete-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance --force
```

Notes:

- `render-ks`, `diff`, and `apply-ks` support interactive `ks.yaml` selection if no path is provided.
- `diff` uses `kubectl diff --server-side --field-manager=homeops-diff -f -`. Exit status 1 means differences were found and is not treated as an error; no resources are applied.
- `diff` includes rendered-object counts before the unified diff. JSON output contains `changed`, `added`, and `diff` fields.
- Local rendering includes the same SOPS and cluster-config substitutions as `render-ks`. It cannot predict Flux pruning, controller-generated objects, admission effects outside the dry-run response, or unavailable external sources. Secret data is masked by kubectl by default.
- `delete-ks` requires an explicit `ks.yaml` path.
- Use `--name` when one `ks.yaml` file contains multiple Flux `Kustomization` documents.

## VolSync

### Controller State

```bash
homeops-cli volsync state            # show: kustomization / helmrelease / deployment
homeops-cli volsync state suspend
homeops-cli volsync state resume
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

homeops-cli volsync snapshots --app paperless
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

homeops-cli volsync verify --namespace default --app paperless --yes
homeops-cli volsync verify --namespace default --app paperless --check --timeout 20m
homeops-cli volsync verify --namespace default --app paperless --output json --yes
```

Notes:

- `restore` prompts for namespace, application, and snapshot when omitted.
- `restore-all` is the bulk restore workflow for a namespace or broader recovery operation; use `--help` before running it on live workloads.
- `verify` restores the latest snapshot into an ownerless scratch PVC with the app PVC's storage class and capacity. It never modifies the app PVC or ReplicationSource.
- `verify --check` mounts the scratch PVC read-only in a temporary Alpine pod, lists a sample of regular files, and reports `du -sh` output. An empty filesystem fails verification.
- Verification always attempts cleanup in pod, ReplicationDestination, PVC order after success, failure, timeout, or interrupt. Existing same-app scratch objects cause refusal unless `--force` is supplied. Resource creation still requires confirmation; global `--yes` bypasses the prompt.

## Workstation

```bash
homeops-cli workstation setup             # detect OS, scan tools, pick what to install
homeops-cli workstation setup --all       # install everything missing (CI-friendly)
homeops-cli workstation setup --all --upgrade   # ...and upgrade installed tools to latest
homeops-cli workstation setup --dry-run   # status table only
homeops-cli workstation brew              # apply the embedded Brewfile wholesale
homeops-cli workstation krew              # install kubectl plugins
```

`setup` detects the platform (macOS / Linux distro, architecture, Homebrew
availability), scans a curated catalog (kubectl, helm, helmfile, flux,
talosctl, cilium, k9s, jq, yq, gh, op, ...) with installed versions, and
installs the selection through Homebrew — the one package manager carrying
all of these at their latest versions on both macOS and Linux. macOS-only
casks (1password-cli) are marked unavailable on Linux with a hint instead of
failing.

## Completion

```bash
homeops-cli completion bash
homeops-cli completion zsh
homeops-cli completion fish
homeops-cli completion powershell
```

For shell-specific setup, see [`COMPLETION.md`](./COMPLETION.md).

## Practical Workflows

### Deploy Flatcar nodes on Proxmox (current)

```bash
homeops-cli flatcar deploy-vm --nodes k8s-0,k8s-1,k8s-2 --concurrency 3
```

### Prepare and Deploy Talos VMs on Proxmox (legacy)

```bash
homeops-cli talos prepare-iso
homeops-cli talos deploy-vm --name k8s --node-count 3 --concurrency 3 --generate-iso
```

### Inspect a Secret Interactively

```bash
homeops-cli k8s view-secret
```

### Render and Apply a Local Flux Kustomization

```bash
homeops-cli k8s render-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s diff ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
homeops-cli k8s apply-ks ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
```

### Snapshot and Restore an Application

```bash
homeops-cli volsync snapshot --namespace default --app paperless
homeops-cli volsync verify --namespace default --app paperless --check --yes
homeops-cli volsync restore --namespace default --app paperless --previous 12
```

## Maintenance Notes

- This file is the single source of truth for command inventory.
- If a command changes in `cmd/`, update this file in the same change.
- Prefer documenting stable command shapes and high-signal flags here. Let `--help` carry the full flag surface.
