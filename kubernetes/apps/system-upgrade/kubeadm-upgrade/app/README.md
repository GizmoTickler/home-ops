# kubeadm-upgrade (system-upgrade-controller Plan)

GitOps-driven **minor** Kubernetes upgrades for the Flatcar + kubeadm control plane,
via [system-upgrade-controller](https://github.com/rancher/system-upgrade-controller) (SUC).

Patch-level k8s sysext updates are automatic (`systemd-sysupdate -C kubernetes` +
`kured`); Flatcar OS releases are merge-gated via the sibling `flatcar-upgrade` Plan.
This Plan covers the remaining piece: a deliberate Kubernetes **minor** bump
(e.g. `v1.36 → v1.37`), which requires the `kubeadm upgrade` dance.

## ⚠️ Status: UNVALIDATED — dormant by default

This Plan has **not yet been run** on this cluster. It is **dormant**: the node selector
requires a label (`homeops.io/kubeadm-upgrade=enabled`) that no node carries, so SUC
selects nothing and creates no Jobs. (Version-pinning alone would NOT be dormant — SUC
baselines a selected node by running the Plan once even if versions already match.)

The upgrade script (in `plan.yaml`) is best-effort and **must be watched on first use**.
It is written **fail-safe**: it verifies the new `kubeadm` is active from the sysext
*before* it touches the cluster, so a wrong sysext step leaves the node drained-but-intact
(recoverable), not broken. Validate the `systemd-sysupdate --definitions=...` invocation
against a real Flatcar node before trusting it unattended.

## How it works

SUC runs a privileged Job per selected node (HostPID, host rootfs at `/host`),
`concurrency: 1`, cordon + drain first. The Job `chroot /host` and, on the host:

1. Repoint the Kubernetes sysext at the target minor (`kubernetes-<minor>.conf`) and
   `systemd-sysupdate ... update` + `systemd-sysext refresh` to fetch the new binaries.
2. Verify `kubeadm` is now the target version (else abort before any cluster change).
3. First control-plane node → `kubeadm upgrade apply -y <target>`; the rest →
   `kubeadm upgrade node` (detected via the `kubeadm-config` ClusterConfiguration version).
4. `systemctl restart kubelet`. A reboot, if the sysext flags one, is handled by `kured`.

## How to perform an upgrade

1. Edit `plan.yaml`: set `spec.version` to the target (e.g. `v1.37.4`). Commit + push.
2. Arm the nodes (one at a time for caution, or all three):
   ```sh
   kubectl label node k8s-0 homeops.io/kubeadm-upgrade=enabled
   ```
3. Watch: `kubectl -n system-upgrade get jobs,pods -w` and the SUC controller logs.
   SUC cordons/drains the node, runs the Job, uncordons on success.
4. When all nodes are done, remove the label to disarm:
   ```sh
   kubectl label node k8s-0 k8s-1 k8s-2 homeops.io/kubeadm-upgrade-
   ```

If a Job fails, the node stays cordoned. Inspect the Job pod logs, fix, then
`kubectl -n system-upgrade delete job <name>` (SUC recreates) or uncordon manually.
