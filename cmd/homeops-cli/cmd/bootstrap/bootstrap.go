package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/logger"
)

type BootstrapConfig struct {
	RootDir       string
	KubeConfig    string
	TalosConfig   string
	K8sVersion    string
	TalosVersion  string
	DryRun        bool
	SkipCRDs      bool
	SkipResources bool
	SkipHelmfile  bool
	SkipPreflight bool
}

type PreflightResult struct {
	Name    string
	Status  string
	Message string
	Error   error
}

func NewCommand() *cobra.Command {
	var config BootstrapConfig

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap Talos nodes and Cluster applications",
		Long: `Bootstrap a complete Talos cluster including:
- Applying Talos configuration to all nodes
- Bootstrapping the cluster
- Installing CRDs and resources
- Syncing Helm releases`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// This will run all subcommands in order
			return runBootstrap(cmd, &config)
		},
	}

	// Add flags
	cmd.Flags().StringVar(&config.RootDir, "root-dir", ".", "Root directory of the project")
	cmd.Flags().StringVar(&config.KubeConfig, "kubeconfig", os.Getenv("KUBECONFIG"), "Path to kubeconfig file")
	cmd.Flags().StringVar(&config.TalosConfig, "talosconfig", os.Getenv("TALOSCONFIG"), "Path to talosconfig file")
	cmd.Flags().StringVar(&config.K8sVersion, "k8s-version", os.Getenv("KUBERNETES_VERSION"), "Kubernetes version")
	cmd.Flags().StringVar(&config.TalosVersion, "talos-version", os.Getenv("TALOS_VERSION"), "Talos version")
	cmd.Flags().BoolVar(&config.DryRun, "dry-run", false, "Perform a dry run without making changes")
	cmd.Flags().BoolVar(&config.SkipCRDs, "skip-crds", false, "Skip CRD installation")
	cmd.Flags().BoolVar(&config.SkipResources, "skip-resources", false, "Skip resource creation")
	cmd.Flags().BoolVar(&config.SkipHelmfile, "skip-helmfile", false, "Skip Helmfile sync")
	cmd.Flags().BoolVar(&config.SkipPreflight, "skip-preflight", false, "Skip preflight checks (not recommended)")

	// Add subcommands
	cmd.AddCommand(
		newPreflightCommand(&config),
		newApplyConfigCommand(&config),
		newClusterBootstrapCommand(&config),
		newKubeconfigCommand(&config),
		newWaitNodesCommand(&config),
		newApplyManifestsCommand(&config),
		newSyncCommand(&config),
	)

	return cmd
}

func runBootstrap(cmd *cobra.Command, config *BootstrapConfig) error {
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}
	log.Info("🚀 Starting cluster bootstrap process")

	// Convert RootDir to absolute path if it's relative
	if !filepath.IsAbs(config.RootDir) {
		absPath, err := filepath.Abs(config.RootDir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for root directory: %w", err)
		}
		config.RootDir = absPath
	}

	log.Debugf("Using kubeconfig: %s", config.KubeConfig)
	log.Debugf("Using talosconfig: %s", config.TalosConfig)

	// Load versions from system-upgrade plans if not provided via flags/env
	if config.K8sVersion == "" || config.TalosVersion == "" {
		versionConfig := versionconfig.GetVersions(config.RootDir)
		if config.K8sVersion == "" {
			config.K8sVersion = versionConfig.KubernetesVersion
		}
		if config.TalosVersion == "" {
			config.TalosVersion = versionConfig.TalosVersion
		}
		log.Debugf("Loaded versions from system-upgrade plans: K8s=%s, Talos=%s", config.K8sVersion, config.TalosVersion)
	}

	// Define the order of execution
	subcommands := []string{
		"preflight",
		"apply-config",
		"cluster-bootstrap",
		"kubeconfig",
		"wait-nodes",
		"apply-manifests",
		"sync",
	}

	for _, sub := range subcommands {
		subCmd, _, err := cmd.Find([]string{sub})
		if err != nil {
			return fmt.Errorf("subcommand %s not found: %w", sub, err)
		}

		if subCmd.RunE != nil {
			if err := subCmd.RunE(subCmd, []string{}); err != nil {
				return err
			}
		}
	}

	log.Infof("✅ 🎉 Congrats! The cluster is bootstrapped and Flux is syncing the Git repository")
	return nil
}
