// Package cluster implements end-to-end cluster assurance workflows that span
// Kubernetes, Flatcar/kubeadm, and the configured hypervisor.
package cluster

import "github.com/spf13/cobra"

// NewCommand creates the top-level cluster command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Run end-to-end cluster assurance workflows",
	}
	cmd.AddCommand(newRehearseNodeCommand())
	return cmd
}
