package bootstrap

import (
	"bytes"
	"fmt"
	"strings"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"
)

// applyNamespaces creates the initial namespaces required for bootstrap
func applyNamespaces(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would apply initial namespaces")
		return nil
	}

	// Define all namespaces used in the cluster
	// This ensures all namespaces exist before any resources are applied
	namespaces := initialBootstrapNamespaces()

	for _, ns := range namespaces {
		logger.Debug("Creating namespace: %s", ns)

		manifestOutput, err := bootstrapKubectlOutput(config, "create", "namespace", ns, "--dry-run=client", "-o", "yaml")
		if err != nil {
			return fmt.Errorf("failed to generate namespace manifest for %s: %w", ns, err)
		}

		if output, err := bootstrapKubectlCombinedIn(config, bytes.NewReader(manifestOutput), "apply", "--filename", "-"); err != nil {
			// Check if namespace already exists - that's okay
			if strings.Contains(string(output), "AlreadyExists") || strings.Contains(string(output), "unchanged") {
				logger.Debug("Namespace %s already exists", ns)
				continue
			}
			return fmt.Errorf("failed to create namespace %s: %w\nOutput: %s", ns, err, redactCommandOutput(output))
		}

		logger.Debug("Successfully created namespace: %s", ns)
	}

	return nil
}

func applyResources(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get resources from embedded YAML file (no longer a template)
	resources, err := bootstrapGetBootstrapFile("resources.yaml")
	if err != nil {
		return fmt.Errorf("failed to get resources: %w", err)
	}

	// Resolve 1Password references in the YAML content
	logger.Info("Resolving 1Password references in bootstrap resources...")
	resolvedResources, err := bootstrapResolveSecrets(resources, logger)
	if err != nil {
		return fmt.Errorf("failed to resolve 1Password references: %w", err)
	}

	if config.DryRun {
		// Validate YAML content and 1Password references
		if err := validateResourcesYAML(resolvedResources, logger); err != nil {
			return fmt.Errorf("resources validation failed: %w", err)
		}
		logger.Info("[DRY RUN] Resources validation passed - would apply resources")
		return nil
	}

	// Apply resources with force-conflicts to handle cert-manager managed fields
	if output, err := bootstrapKubectlCombinedIn(config, bytes.NewReader([]byte(resolvedResources)), "apply", "--server-side", "--force-conflicts", "--filename", "-"); err != nil {
		return fmt.Errorf("failed to apply resources: %w\n%s", err, redactCommandOutput(output))
	}

	logger.Info("Resources applied successfully")
	return nil
}

func applyClusterSecretStore(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Get cluster secret store from embedded YAML file (no longer a template)
	clusterSecretStore, err := bootstrapGetBootstrapFile("clustersecretstore.yaml")
	if err != nil {
		return fmt.Errorf("failed to get cluster secret store: %w", err)
	}

	// Resolve 1Password references in the YAML content
	logger.Info("Resolving 1Password references in cluster secret store...")
	resolvedClusterSecretStore, err := bootstrapResolveSecrets(clusterSecretStore, logger)
	if err != nil {
		return fmt.Errorf("failed to resolve 1Password references: %w", err)
	}

	if config.DryRun {
		// Validate YAML content and 1Password references
		if err := validateClusterSecretStoreYAML(resolvedClusterSecretStore, logger); err != nil {
			return fmt.Errorf("cluster secret store validation failed: %w", err)
		}
		logger.Info("[DRY RUN] Cluster secret store validation passed - would apply cluster secret store")
		return nil
	}

	// Apply cluster secret store with force-conflicts to handle field management conflicts
	if output, err := bootstrapKubectlCombinedIn(config, bytes.NewReader([]byte(resolvedClusterSecretStore)),
		"apply", "--namespace", constants.NSExternalSecret, "--server-side", "--force-conflicts", "--filename", "-", "--wait=true"); err != nil {
		return fmt.Errorf("failed to apply cluster secret store: %w\n%s", err, redactCommandOutput(output))
	}

	logger.Info("Cluster secret store applied successfully")
	return nil
}
