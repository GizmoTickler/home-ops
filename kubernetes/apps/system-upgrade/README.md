# System-Upgrade (Fedora CoreOS / Flatcar + kubeadm)

Automated Kubernetes minor-upgrades, merge-gated OS releases and node reboot
coordination for the kubeadm control plane. The nodes run **Fedora CoreOS**
(`homeops.io/os=fcos`); the Flatcar Plan is kept for Flatcar-provisioned nodes
and as homeops-cli's Flatcar version source.

## Architecture

Three components, all deployed to the `system-upgrade` namespace:

| Component | Purpose | Control point |
|---|---|---|
| **system-upgrade-controller** | Runs privileged Jobs on selected nodes via `Plan` CRs. | `system-upgrade-controller/app/` |
| **kubeadm-upgrade** (Plan) | Orchestrates `kubeadm upgrade` (minor bumps) via sysext swap → drain → upgrade → kubelet restart. | `kubeadm-upgrade/app/plan.yaml` (version) |
| **fcos-upgrade** (Plan) | Merge-gated Fedora CoreOS releases: stages the pinned version via `rpm-ostree rebase` (Zincati auto-updates are disabled on the nodes). | `fcos-upgrade/app/plan.yaml` (version) |
| **flatcar-upgrade** (Plan) | Merge-gated Flatcar OS releases: stages the pinned version via `flatcar-update` (channel polling is disabled on the nodes). | `flatcar-upgrade/app/plan.yaml` (version) |
| **kured** | Coordinates reboots when rpm-ostree has a pending deployment, a Flatcar OS update is staged, or `/run/reboot-required` exists. One-at-a-time, no lock auto-expiry. | `kured/app/helmrelease.yaml` (Helm) |

## Upgrade Flow

### Automatic (Kubernetes patch-level only — Flatcar nodes)

1. `systemd-sysupdate.timer` checks for new Kubernetes sysext patches
   within the pinned minor (`systemd-sysupdate -C kubernetes update`).
2. When the sysext version changes, `systemd-sysupdate.service` touches
   `/run/reboot-required`.
3. `kured` picks up the reboot sentinel, cordons+reboots one node at a time.

### GitOps-driven (Fedora CoreOS releases)

Zincati auto-updates are **disabled** (`/etc/zincati/config.d/90-disable-auto-updates.toml`,
set via Butane) — the OS only moves on a merge:

1. Renovate (`custom.fcos`, never automerged) or a manual edit bumps
   `spec.version` in `fcos-upgrade/app/plan.yaml` to a stable release; merge it.
2. SUC runs a staging Job per node (one at a time, no cordon): `rpm-ostree rebase
   ostree-image-signed:docker://quay.io/fedora/fedora-coreos:<version>` stages the
   signed image as the next deployment (layered packages carry over).
3. `kured` sees the pending deployment (`rpm-ostree status --json`:
   `deployments[0]` is not booted), cordons+drains+reboots one node at a time.
   greenboot rolls a failed boot back to the previous deployment.

### GitOps-driven (Flatcar OS releases)

`update_engine` channel polling is **disabled** (`SERVER=disabled` in
`/etc/flatcar/update.conf`, set via Ignition) — the OS only moves on a merge:

1. Bump `spec.version` in `flatcar-upgrade/app/plan.yaml` to a release from
   <https://www.flatcar.org/releases> and merge.
2. SUC runs a staging Job per node (one at a time, no cordon): `flatcar-update
   --to-version <target>` downloads + verifies the payload into the inactive
   USR partition, leaving update_engine in `UPDATE_STATUS_UPDATED_NEED_REBOOT`.
3. `kured` detects that status, cordons+drains+reboots one node at a time.

No label gate: the Job no-ops when a node is already at (or has staged) the
target, so SUC's baseline run is harmless.

### GitOps-driven (minor upgrades, e.g. v1.36 → v1.37)

1. Bump `spec.version` in `kubeadm-upgrade/app/plan.yaml` and commit.
2. Label a node: `kubectl label node k8s-0 homeops.io/kubeadm-upgrade=enabled`
3. SUC cordons + drains the node, runs the Job (which `chroot /host`):
   - FCOS: rebuilds the directory sysext via `/usr/local/bin/homeops-install-k8s-sysext <version>`
   - Flatcar: repoints the sysext at the new minor and runs
     `systemd-sysupdate --definitions=/etc/sysupdate.kubernetes.d update`
   - Fail-safe: verifies `kubeadm version` matches target before touching cluster
   - `kubeadm upgrade apply` (first node) or `upgrade node` (rest)
   - `systemctl restart kubelet`
4. Remove the label to disarm: `kubectl label node k8s-0 homeops.io/kubeadm-upgrade-`

See `kubeadm-upgrade/app/README.md` for the full trigger procedure.

## Version Source

The homeops CLI reads the Kubernetes target from
`kubeadm-upgrade/app/plan.yaml` `spec.version` and the Flatcar OS target from
`flatcar-upgrade/app/plan.yaml` `spec.version` — these are the single source
of truth for the GitOps Plans and the `homeops-cli flatcar render-ignition`
/ `gen-kubeadm` / provisioning commands. There is no separate `versions.env`
or tuppr CRD.

## Live Verification

```bash
# Plan and controller health
kubectl -n system-upgrade get deploy,ds,plan -o wide

# Confirm Plan is dormant (no node labels = no Jobs)
kubectl get nodes -L homeops.io/kubeadm-upgrade

# Flux reconciliation status
kubectl -n system-upgrade get kustomizations
```
