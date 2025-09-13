package bootstrap

import (
	"fmt"

	"github.com/spf13/cobra"
	"homeops-cli/internal/logger"
)

func newWaitNodesCommand(config *BootstrapConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait-nodes",
		Short: "Wait for the Kubernetes nodes to be ready",
		Long:  `This command waits for the Kubernetes nodes to be ready.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := logger.New()
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}

			log.Info("⏳ Waiting for nodes to be ready")
			if err := waitForNodes(config, log); err != nil {
				return fmt.Errorf("failed waiting for nodes: %w", err)
			}
			log.Infof("✅ All nodes are ready")
			return nil
		},
	}
	return cmd
}
