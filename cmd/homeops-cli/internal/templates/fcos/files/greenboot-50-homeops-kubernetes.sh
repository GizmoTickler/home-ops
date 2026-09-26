#!/usr/bin/bash
# /etc/greenboot/check/required.d/50-homeops-kubernetes.sh — greenboot REQUIRED
# health check for a Fedora CoreOS kubeadm node.
#
# A boot is only marked green when containerd is active and, once the node has
# joined the cluster (kubeadm wrote /etc/kubernetes/kubelet.conf), kubelet is
# active too. Before the join kubelet crash-loops by design (no config yet), so
# it is not required then. A failing required check makes greenboot mark the
# boot red; after GREENBOOT_MAX_BOOT_ATTEMPTS it rolls back to the previous
# rpm-ostree deployment — the FCOS stand-in for Flatcar's A/B partition
# fallback.
#
# greenboot-healthcheck.service is not ordered after either unit, so wait (up
# to 5 minutes) rather than failing a boot that is merely still starting.
set -uo pipefail

deadline=$((SECONDS + 300))

wait_active() {
	local unit="$1"
	until systemctl is-active --quiet "${unit}"; do
		if ((SECONDS >= deadline)); then
			echo "homeops: ${unit} is not active after 300s" >&2
			return 1
		fi
		sleep 5
	done
	echo "homeops: ${unit} is active"
}

wait_active containerd.service || exit 1
if [[ -e /etc/kubernetes/kubelet.conf ]]; then
	wait_active kubelet.service || exit 1
fi
exit 0
