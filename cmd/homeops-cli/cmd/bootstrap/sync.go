package bootstrap

import (
	"fmt"

	"github.com/spf13/cobra"
	"homeops-cli/internal/logger"
)

func newSyncCommand(config *BootstrapConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync Helm releases",
		Long:  `This command syncs the Helm releases in the cluster.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := logger.New()
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}

			if !config.SkipHelmfile {
				log.Info("⚙️  Syncing Helm releases")
				if err := syncHelmReleases(config, log); err != nil {
					return fmt.Errorf("failed to sync Helm releases: %w", err)
				}
				log.Infof("✅ Helm releases synced successfully")
			}
			return nil
		},
	}
	return cmd
}
