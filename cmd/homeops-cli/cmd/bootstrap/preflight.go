package bootstrap

import (
	"fmt"

	"github.com/spf13/cobra"
	"homeops-cli/internal/logger"
)

func newPreflightCommand(config *BootstrapConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Run preflight checks to validate the environment",
		Long:  `This command runs a series of preflight checks to ensure that the environment is correctly configured for a successful bootstrap.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := logger.New()
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}

			log.Info("🔍 Running preflight checks...")
			if err := runPreflightChecks(config, log); err != nil {
				return fmt.Errorf("preflight checks failed: %w", err)
			}
			log.Infof("✅ All preflight checks passed")
			return nil
		},
	}
	return cmd
}
