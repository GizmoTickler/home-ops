package flatcar

import "strings"

// KubernetesMinor derives "vX.Y" from "vX.Y.Z". Shared by the cmd/flatcar and
// cmd/bootstrap node-env builders (previously duplicated in both).
func KubernetesMinor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}
