package bootstrap

import (
	"fmt"

	"github.com/spf13/cobra"
	"homeops-cli/internal/logger"
)

func newApplyManifestsCommand(config *BootstrapConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply-manifests",
		Short: "Apply initial Kubernetes manifests",
		Long:  `This command applies the initial Kubernetes manifests, including namespaces, resources, and CRDs.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, err := logger.New()
			if err != nil {
				return fmt.Errorf("failed to create logger: %w", err)
			}

			log.Info("📦 Creating initial namespaces")
			if err := applyNamespaces(config, log); err != nil {
				return fmt.Errorf("failed to apply namespaces: %w", err)
			}
			log.Infof("✅ Initial namespaces created successfully")

			if !config.SkipResources {
				log.Info("🔧 Applying initial resources")
				if err := applyResources(config, log); err != nil {
					return fmt.Errorf("failed to apply resources: %w", err)
				}
				log.Infof("✅ Initial resources applied successfully")
			}

			if !config.SkipCRDs {
				log.Info("📜 Applying Custom Resource Definitions")
				if err := applyCRDs(config, log); err != nil {
					return fmt.Errorf("failed to apply CRDs: %w", err)
				}
				log.Infof("✅ CRDs applied successfully")
			}
			return nil
		},
	}
	return cmd
}
