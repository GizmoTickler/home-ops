package bootstrap

import (
	"fmt"

	"github.com/spf13/cobra"
	"homeops-cli/internal/logger"
)

func newApplyConfigCommand(config *BootstrapConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply-config",
		Short: "Apply Talos configuration to the nodes",
		Long:  `This command applies the Talos configuration to the nodes in the cluster.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := logger.New()
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}

			log.Info("📋 Applying Talos configuration to nodes")
			if err := applyTalosConfig(config, log); err != nil {
				return fmt.Errorf("failed to apply Talos config: %w", err)
			}
			log.Infof("✅ Talos configuration applied successfully")
			return nil
		},
	}
	return cmd
}
