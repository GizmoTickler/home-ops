package kubeutil

import "strings"

// OwnerReference is the metadata subset needed by PVC hygiene checks.
type OwnerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// IsVolSyncPlumbingPVC identifies mover/cache PVCs that are implementation
// details rather than application data volumes.
func IsVolSyncPlumbingPVC(name string, labels map[string]string, ownerReferences []OwnerReference) bool {
	for _, ref := range ownerReferences {
		if (ref.Kind == "ReplicationSource" || ref.Kind == "ReplicationDestination") &&
			strings.Contains(ref.APIVersion, "volsync.backube") {
			return true
		}
	}
	for _, key := range []string{
		"app.kubernetes.io/created-by",
		"app.kubernetes.io/managed-by",
		"volsync.backube/created-by",
	} {
		if strings.EqualFold(labels[key], "volsync") {
			return true
		}
	}
	return strings.HasPrefix(name, "volsync-") &&
		(strings.HasSuffix(name, "-src") || strings.HasSuffix(name, "-cache"))
}

// IsPodOwnedPVC identifies ephemeral PVCs created for a Pod.
func IsPodOwnedPVC(ownerReferences []OwnerReference) bool {
	for _, ref := range ownerReferences {
		if ref.Kind == "Pod" {
			return true
		}
	}
	return false
}

// HasWorkloadOwner identifies PVCs whose lifecycle is explicitly controlled by
// a workload or storage/backup controller.
func HasWorkloadOwner(ownerReferences []OwnerReference) bool {
	for _, ref := range ownerReferences {
		switch ref.Kind {
		case "Pod", "StatefulSet", "Deployment", "ReplicaSet", "DaemonSet", "Job", "CronJob",
			"ReplicationSource", "ReplicationDestination":
			return true
		}
	}
	return false
}

// IsSystemNamespace returns true for namespaces whose PVCs are infrastructure,
// not application backup targets.
func IsSystemNamespace(namespace string) bool {
	if strings.HasSuffix(namespace, "-system") {
		return true
	}
	switch namespace {
	case "kube-public", "kube-node-lease", "rook-ceph", "volsync":
		return true
	default:
		return false
	}
}
