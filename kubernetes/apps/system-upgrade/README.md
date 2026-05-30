# System-Upgrade (Flatcar/kubeadm)

Automated Kubernetes minor-upgrades and node reboot coordination for the
Flatcar Container Linux + kubeadm control plane.

## Architecture

Three components, all deployed to the `system-upgrade` namespace:

| Component | Purpose | Control point |
|---|---|---|
| **system-upgrade-controller** | Runs privileged Jobs on selected nodes via `Plan` CRs. | `system-upgrade-controller/app/` |
| **kubeadm-upgrade** (Plan) | Orchestrates `kubeadm upgrade` (minor bumps) via sysext swap → drain → upgrade → kubelet restart. | `kubeadm-upgrade/app/plan.yaml` (version) |
| **kured** | Coordinates reboots when a sysext or Flatcar OS update flags a reboot. One-at-a-time, 30m lock TTL. | `kured/app/helmrelease.yaml` (Helm) |

## Upgrade Flow

### Automatic (patch-level)

1. `systemd-sysupdate.timer` checks for new Kubernetes sysext patches
   and Flatcar OS updates.
2. When the sysext version changes, `systemd-sysupdate.service` touches
   `/run/reboot-required`.
3. `kured` picks up the reboot sentinel, cordons+reboots one node at a time.

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
`kubeadm-upgrade/app/plan.yaml` `spec.version` — this is the single source
of truth for both the GitOps Plan and the `homeops-cli flatcar render-ignition`
/ `gen-kubeadm` commands. There is no separate `versions.env` or tuppr CRD.

## Live Verification

```bash
# Plan and controller health
kubectl -n system-upgrade get deploy,ds,plan -o wide

# Confirm Plan is dormant (no node labels = no Jobs)
kubectl get nodes -L homeops.io/kubeadm-upgrade

# Flux reconciliation status
kubectl -n system-upgrade get kustomizations
```
