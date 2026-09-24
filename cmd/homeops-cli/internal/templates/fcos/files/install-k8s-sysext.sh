#!/usr/bin/bash
# /usr/local/bin/homeops-install-k8s-sysext — build and activate the Kubernetes
# systemd-sysext on a Fedora CoreOS node.
#
# The Flatcar nodes activate a prebuilt sysext-bakery Kubernetes image, but those
# images carry ID=flatcar in their extension-release and systemd-sysext refuses
# to merge them on FCOS. Instead this builds a plain DIRECTORY extension (FCOS
# ships systemd with directory-extension support) holding the same payload the
# Flatcar sysext-bakery kubernetes image provides:
#
#   usr/bin/{kubelet,kubeadm,kubectl}   dl.k8s.io release binaries, sha256-verified
#   usr/bin/crictl                      cri-tools release (kubeadm preflight needs it)
#   usr/libexec/cni/*                   containernetworking plugins tarball; kubelet's
#                                       20-homeops-fcos.conf copies them into
#                                       /opt/cni/bin (containerd's default CNI bin
#                                       dir; Multus NADs rely on macvlan/static/sbr
#                                       there). Flatcar's image uses usr/local/bin/cni,
#                                       but /usr/local is a symlink to /var/usrlocal on
#                                       FCOS and an extension directory there would
#                                       shadow it, so the Fedora packaging path is used.
#   usr/share/kubernetes/kubernetes-version  copied to /etc/kubernetes-version
#   usr/libexec/kubernetes/kubelet-plugins/volume/exec -> /var/kubernetes/...
#                                       the flex-volume dir kubeadm mounts into
#                                       kube-controller-manager; /usr is read-only,
#                                       so (like the Flatcar image) it points at /var.
#
# The extension-release declares ID=_any so it merges regardless of the host OS
# version; the payload is statically linked upstream binaries.
#
# Idempotent: install-k8s-sysext.service only runs while
# /var/lib/homeops/k8s-sysext-<version>.stamp is absent, and the payload is
# staged on the same filesystem and swapped in with a rename, so an interrupted
# run never leaves a half-populated extension behind.
#
# Kubernetes upgrade path (no systemd-sysupdate on FCOS): drain the node, run
#   sudo /usr/local/bin/homeops-install-k8s-sysext v<new-version>
# which unmerges, replaces the directory contents and runs
# `systemd-sysext refresh`, then run `kubeadm upgrade apply|node` and restart
# kubelet. No reboot is needed for a binary swap, so /run/reboot-required is
# deliberately NOT touched (Kured would otherwise reboot the node for nothing).
set -euo pipefail

K8S_VERSION="${1:-{{ ENV.KUBERNETES_VERSION }}}"
CRICTL_VERSION="{{ ENV.CRICTL_VERSION }}"
CNI_VERSION="{{ ENV.CNI_PLUGINS_VERSION }}"
ARCH=amd64

EXT_DIR=/var/lib/extensions/kubernetes
STATE_DIR=/var/lib/homeops
STAMP="${STATE_DIR}/k8s-sysext-${K8S_VERSION}.stamp"

mkdir -p "${STATE_DIR}" /var/lib/extensions
# Stage beside the extension (same /var filesystem) so the final mv is an
# atomic rename; systemd-sysext never sees a partially written tree.
STAGE="$(mktemp -d "${STATE_DIR}/k8s-sysext-staging.XXXXXX")"
DL="$(mktemp -d "${STATE_DIR}/k8s-sysext-download.XXXXXX")"
trap 'rm -rf "${STAGE}" "${DL}"' EXIT
# mktemp creates 0700; the extension root must be traversable like /usr.
chmod 0755 "${STAGE}"

fetch() {
	curl --fail --silent --show-error --location \
		--retry 6 --retry-delay 5 --retry-connrefused \
		--output "$2" "$1"
}

# verify <file> <checksum-file>: every upstream .sha256 used here carries the
# hex digest as its first field (dl.k8s.io: digest only; cri-tools and CNI:
# "<digest>  <name>").
verify() {
	local want
	want="$(awk '{print $1; exit}' "$2")"
	if [[ ! "${want}" =~ ^[0-9a-f]{64}$ ]]; then
		echo "homeops: malformed checksum file $2" >&2
		return 1
	fi
	echo "${want}  $1" | sha256sum --check --strict --quiet
}

mkdir -p \
	"${STAGE}/usr/bin" \
	"${STAGE}/usr/libexec/cni" \
	"${STAGE}/usr/libexec/kubernetes/kubelet-plugins/volume" \
	"${STAGE}/usr/share/kubernetes" \
	"${STAGE}/usr/lib/extension-release.d"

for bin in kubelet kubeadm kubectl; do
	url="https://dl.k8s.io/release/${K8S_VERSION}/bin/linux/${ARCH}/${bin}"
	fetch "${url}" "${STAGE}/usr/bin/${bin}"
	fetch "${url}.sha256" "${DL}/${bin}.sha256"
	verify "${STAGE}/usr/bin/${bin}" "${DL}/${bin}.sha256"
	chmod 0755 "${STAGE}/usr/bin/${bin}"
done

crictl_tgz="crictl-${CRICTL_VERSION}-linux-${ARCH}.tar.gz"
crictl_url="https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/${crictl_tgz}"
fetch "${crictl_url}" "${DL}/${crictl_tgz}"
fetch "${crictl_url}.sha256" "${DL}/${crictl_tgz}.sha256"
verify "${DL}/${crictl_tgz}" "${DL}/${crictl_tgz}.sha256"
tar --no-same-owner -xzf "${DL}/${crictl_tgz}" -C "${STAGE}/usr/bin" crictl
chmod 0755 "${STAGE}/usr/bin/crictl"

cni_tgz="cni-plugins-linux-${ARCH}-${CNI_VERSION}.tgz"
cni_url="https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/${cni_tgz}"
fetch "${cni_url}" "${DL}/${cni_tgz}"
fetch "${cni_url}.sha256" "${DL}/${cni_tgz}.sha256"
verify "${DL}/${cni_tgz}" "${DL}/${cni_tgz}.sha256"
tar --no-same-owner -xzf "${DL}/${cni_tgz}" -C "${STAGE}/usr/libexec/cni"

ln -s /var/kubernetes/kubelet-plugins/volume/exec \
	"${STAGE}/usr/libexec/kubernetes/kubelet-plugins/volume/exec"
echo "${K8S_VERSION}" >"${STAGE}/usr/share/kubernetes/kubernetes-version"
echo "${CNI_VERSION}" >"${STAGE}/usr/share/kubernetes/kubernetes-cni-version"
printf 'ID=_any\n' >"${STAGE}/usr/lib/extension-release.d/extension-release.kubernetes"

# FCOS runs SELinux enforcing. Files created under /var/lib carry var_lib_t,
# and the overlay systemd-sysext mounts on /usr exposes the lower layer's
# labels, so label the staged tree exactly as if it lived at / (kubelet gets
# kubelet_exec_t, the rest bin_t/usr_t) — what an RPM install would produce.
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
	seltype="$(sed -n 's/^SELINUXTYPE=//p' /etc/selinux/config)"
	setfiles -F -r "${STAGE}" \
		"/etc/selinux/${seltype:-targeted}/contexts/files/file_contexts" "${STAGE}"
fi

# Unmerge before touching the extension directory: removing files from the
# lower layer of a live overlay is undefined behaviour. A no-op on first boot.
systemd-sysext unmerge
rm -rf "${EXT_DIR}"
mv "${STAGE}" "${EXT_DIR}"
systemd-sysext refresh

rm -f "${STATE_DIR}"/k8s-sysext-*.stamp
touch "${STAMP}"
echo "homeops: kubernetes ${K8S_VERSION} sysext active (crictl ${CRICTL_VERSION}, cni ${CNI_VERSION})"
