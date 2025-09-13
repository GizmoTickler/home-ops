package bootstrap

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/logger"
)

func newClusterBootstrapCommand(config *BootstrapConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster-bootstrap",
		Short: "Bootstrap the Talos cluster",
		Long:  `This command bootstraps the Talos cluster.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := logger.New()
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}

			log.Info("🎯 Bootstrapping Talos cluster")

			// Wait a moment for configurations to be fully processed (following onedr0p's pattern)
			log.Debug("Waiting for configurations to be processed...")
			time.Sleep(5 * time.Second)
			if err := bootstrapTalos(config, log); err != nil {
				return fmt.Errorf("failed to bootstrap Talos: %w", err)
			}
			log.Infof("✅ Talos cluster bootstrapped successfully")
			return nil
		},
	}
	return cmd
}
