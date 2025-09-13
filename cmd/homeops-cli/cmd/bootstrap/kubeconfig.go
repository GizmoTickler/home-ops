package bootstrap

import (
	"fmt"

	"github.com/spf13/cobra"
	"homeops-cli/internal/logger"
)

func newKubeconfigCommand(config *BootstrapConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Fetch and validate the kubeconfig",
		Long:  `This command fetches the kubeconfig from the cluster and validates it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := logger.New()
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}

			log.Info("🔑 Fetching kubeconfig")
			if err := fetchKubeconfig(config, log); err != nil {
				return fmt.Errorf("failed to fetch kubeconfig: %w", err)
			}
			// Validate kubeconfig is working
			if err := validateKubeconfig(config, log); err != nil {
				return fmt.Errorf("kubeconfig validation failed: %w", err)
			}
			log.Infof("✅ Kubeconfig fetched and validated successfully")
			return nil
		},
	}
	return cmd
}
