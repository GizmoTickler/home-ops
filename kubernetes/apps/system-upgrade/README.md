# System-Upgrade (kubeadm nodes)

Upgrade Plans and node reboot coordination for the kubeadm control plane.

> **Current state:** all three nodes (k8s-0/1/2) run **Fedora CoreOS** (rebuilt in
> place on 2026-09-26, `homeops.io/os=fcos`). The two Plans in this directory were
> written for **Flatcar** and do not upgrade FCOS nodes — see
> [Fedora CoreOS nodes](#fedora-coreos-nodes) below. The Flatcar sections are kept
> for reference while the Flatcar provider remains selectable in homeops-cli.

## Architecture

Components deployed to the `system-upgrade` namespace:

| Component | Purpose | Control point |
|---|---|---|
| **system-upgrade-controller** | Runs privileged Jobs on selected nodes via `Plan` CRs. | `system-upgrade-controller/app/` |
| **kubeadm-upgrade** (Plan) | Flatcar-only: orchestrates `kubeadm upgrade` (minor bumps) via sysext swap → drain → upgrade → kubelet restart. Dormant (label-gated). | `kubeadm-upgrade/app/plan.yaml` (version) |
| **flatcar-upgrade** (Plan) | Flatcar-only: merge-gated Flatcar OS releases via `flatcar-update`. Excludes `homeops.io/os=fcos`, so it selects no node today. | `flatcar-upgrade/app/plan.yaml` (version) |
| **kured** | Coordinates reboots, one node at a time, no lock auto-expiry. Reboots when `/run/reboot-required` exists (the sentinel command also checks Flatcar's `update_engine`, which FCOS lacks). | `kured/app/helmrelease.yaml` (Helm) |

## Fedora CoreOS nodes

- **OS updates:** Zincati is disabled (`/etc/zincati/config.d/90-disable-auto-updates.toml`,
  `[updates] enabled = false`), so FCOS never follows the stream or reboots on its
  own. The FCOS Butane template describes a git-pinned `rpm-ostree deploy <build>`
  Plan as the intended merge-gated path, but **no such Plan exists in this repo yet**.
  Check node state with `homeops-cli fcos os-status`.
- **Rollback:** greenboot is layered on first boot with a kubelet/containerd health
  check, standing in for Flatcar's A/B partition fallback.
- **Kubernetes binaries:** a directory systemd-sysext at
  `/var/lib/extensions/kubernetes`, built on first boot by
  `/usr/local/bin/homeops-install-k8s-sysext`. There is no systemd-sysupdate on FCOS,
  so the `kubeadm-upgrade` Plan's sysupdate step does not apply. The documented
  upgrade path is per node: drain, run
  `sudo /usr/local/bin/homeops-install-k8s-sysext v<new-version>`, then
  `kubeadm upgrade apply|node` and restart kubelet (no reboot needed).
- **Node rebuilds:** `homeops-cli cluster replace-node --node <name>` rebuilds a node
  in place onto its configured OS.

## Flatcar upgrade flow (reference)

### Automatic (Kubernetes patch-level only)

1. `systemd-sysupdate.timer` checks for new Kubernetes sysext patches
   within the pinned minor (`systemd-sysupdate -C kubernetes update`).
2. When the sysext version changes, `systemd-sysupdate.service` touches
   `/run/reboot-required`.
3. `kured` picks up the reboot sentinel, cordons+reboots one node at a time.

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
   - Repoints the Kubernetes sysext at the new minor
   - `systemd-sysupdate --definitions=/etc/sysupdate.kubernetes.d update`
   - Fail-safe: verifies `kubeadm version` matches target before touching cluster
   - `kubeadm upgrade apply` (first node) or `upgrade node` (rest)
   - `systemctl restart kubelet`
4. Remove the label to disarm: `kubectl label node k8s-0 homeops.io/kubeadm-upgrade-`

See `kubeadm-upgrade/app/README.md` for the full trigger procedure.

## Version Source

The homeops CLI reads the Kubernetes target from
`kubeadm-upgrade/app/plan.yaml` `spec.version` and the Flatcar OS target from
`flatcar-upgrade/app/plan.yaml` `spec.version` — these are the single source
of truth for the GitOps Plans and the `homeops-cli flatcar|fcos render-ignition`
/ `gen-kubeadm` / provisioning commands. There is no separate `versions.env`
or tuppr CRD.

## Live Verification

```bash
# Plan and controller health
kubectl -n system-upgrade get deploy,ds,plan -o wide

# Node OS family and kubeadm-upgrade arming label
kubectl get nodes -L homeops.io/os -L homeops.io/kubeadm-upgrade

# Flux reconciliation status
kubectl -n system-upgrade get kustomizations
```
