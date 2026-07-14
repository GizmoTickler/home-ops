package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
	"homeops-cli/internal/constants"

	yamlv3 "gopkg.in/yaml.v3"
)

// gatewayAPICRDsKustomizationPath is the path (relative to repo root) to the
// kustomization that installs Gateway API CRDs. The version is extracted from
// this file so Renovate manages it in a single place.
const gatewayAPICRDsKustomizationPath = "kubernetes/apps/network/kgateway/gateway-api-crds/kustomization.yaml"

func applyCRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	// Apply Gateway API CRDs from official kubernetes-sigs release
	// These are not in a Helm chart so they must be applied separately
	if err := bootstrapApplyGatewayCRDs(config, logger); err != nil {
		return fmt.Errorf("failed to apply Gateway API CRDs: %w", err)
	}

	// Apply remaining CRDs from Helm charts via helmfile
	return bootstrapApplyCRDsHelmfile(config, logger)
}

// expectedGatewayAPICRDsHost is the only allowed host for Gateway API CRD URLs.
const expectedGatewayAPICRDsHost = "github.com"

// expectedGatewayAPICRDsPathPrefix is the expected URL path prefix for validation.
const expectedGatewayAPICRDsPathPrefix = "/kubernetes-sigs/gateway-api/releases/download/"

// getGatewayAPICRDsURL reads the Gateway API CRDs install URL from the
// kustomization file so the version is managed by Renovate in one place.
func getGatewayAPICRDsURL(rootDir string) (string, error) {
	kustomizationPath := filepath.Join(rootDir, gatewayAPICRDsKustomizationPath)
	content, err := os.ReadFile(kustomizationPath) // #nosec G304 -- kustomization path is built from the local repository root
	if err != nil {
		return "", fmt.Errorf("failed to read gateway-api-crds kustomization at %s: %w", kustomizationPath, err)
	}

	// Parse the kustomization to extract the GitHub release URL from resources
	var kustomization struct {
		Resources []string `yaml:"resources"`
	}
	if err := yamlv3.Unmarshal(content, &kustomization); err != nil {
		return "", fmt.Errorf("failed to parse gateway-api-crds kustomization: %w", err)
	}

	for _, resource := range kustomization.Resources {
		if strings.Contains(resource, "kubernetes-sigs/gateway-api") {
			if err := validateGatewayAPICRDsURL(resource); err != nil {
				return "", fmt.Errorf("invalid Gateway API CRDs URL in %s: %w", kustomizationPath, err)
			}
			return resource, nil
		}
	}

	return "", fmt.Errorf("gateway-api release URL not found in %s", kustomizationPath)
}

// validateGatewayAPICRDsURL validates that the URL points to the expected
// kubernetes-sigs/gateway-api GitHub release location.
func validateGatewayAPICRDsURL(rawURL string) error {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("malformed URL %q: %w", rawURL, err)
	}
	if parsed.Host != expectedGatewayAPICRDsHost {
		return fmt.Errorf("unexpected host %q, expected %q", parsed.Host, expectedGatewayAPICRDsHost)
	}
	if !strings.HasPrefix(parsed.Path, expectedGatewayAPICRDsPathPrefix) {
		return fmt.Errorf("unexpected path %q, expected prefix %q", parsed.Path, expectedGatewayAPICRDsPathPrefix)
	}
	return nil
}

// extractGatewayAPIVersion extracts the version tag (e.g. "v1.4.1") from a
// validated Gateway API CRDs URL. Returns the full URL as fallback.
func extractGatewayAPIVersion(rawURL string) string {
	rest := strings.TrimPrefix(rawURL, "https://"+expectedGatewayAPICRDsHost+expectedGatewayAPICRDsPathPrefix)
	if rest == rawURL {
		return rawURL
	}
	if idx := strings.Index(rest, "/"); idx > 0 {
		return rest[:idx]
	}
	return rawURL
}

// applyGatewayAPICRDs installs the standard Gateway API CRDs from the official
// kubernetes-sigs/gateway-api GitHub release. These are applied via Kustomize in
// the GitOps flow (kubernetes/apps/network/kgateway/gateway-api-crds/) but need
// to be present before Flux starts reconciling.
func applyGatewayAPICRDs(config *BootstrapConfig, logger *common.ColorLogger) error {
	url, err := getGatewayAPICRDsURL(config.RootDir)
	if err != nil {
		return err
	}

	version := extractGatewayAPIVersion(url)

	// Dry runs must not require a kubeconfig: on a fresh machine it doesn't
	// exist until the real bootstrap fetches it (step 2).
	if config.DryRun {
		logger.Info("[DRY RUN] Would apply Gateway API CRDs %s from %s", version, url)
		return nil
	}
	if config.KubeConfig == "" {
		return fmt.Errorf("kubeconfig path is required for Gateway API CRD installation - ensure KUBECONFIG environment variable is set")
	}

	logger.Info("Applying Gateway API CRDs %s from %s", version, url)

	if output, err := kubectlCombinedOutput(config, "apply", "--server-side", "--filename", url); err != nil {
		return fmt.Errorf("failed to apply Gateway API CRDs %s from %s: %w\nKubectl output: %s", version, url, err, redactCommandOutput(output))
	}

	logger.Success("Gateway API CRDs %s applied successfully", version)
	return nil
}

func applyCRDsFromHelmfile(config *BootstrapConfig, logger *common.ColorLogger) error {
	logger.Info("Applying CRDs from dedicated helmfile...")

	if config.DryRun {
		logger.Info("[DRY RUN] Would apply CRDs from crds/helmfile.yaml")
		return nil
	}

	// Create temporary directory for helmfile execution
	tempDir, err := os.MkdirTemp("", "homeops-crds-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			logger.Warn("Warning: failed to remove temp directory: %v", removeErr)
		}
	}()

	// Get embedded CRDs helmfile content
	crdsHelmfileTemplate, err := bootstrapGetBootstrapFile("helmfile.d/00-crds.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded CRDs helmfile: %w", err)
	}

	// The CRDs helmfile doesn't need templating, write it directly
	crdsHelmfilePath := filepath.Join(tempDir, "00-crds.yaml")
	if err := os.WriteFile(crdsHelmfilePath, []byte(crdsHelmfileTemplate), 0o600); err != nil {
		return fmt.Errorf("failed to write CRDs helmfile: %w", err)
	}

	logger.Info("Using dedicated CRDs helmfile to extract CRDs only")

	// Use helmfile template to generate CRDs only (the helmfile has --include-crds but we filter to CRDs)
	output, err := bootstrapHelmfileTemplateOutput(tempDir, config, crdsHelmfilePath)
	if err != nil {
		return fmt.Errorf("failed to template CRDs from helmfile: %w", err)
	}

	if len(output) == 0 {
		logger.Warn("No manifests generated from CRDs helmfile template")
		return nil
	}

	// Extract only the CRDs from the output
	crdManifests, otherManifests, err := separateCRDsFromManifests(string(output))
	if err != nil {
		return fmt.Errorf("failed to separate CRDs from manifests: %w", err)
	}

	if len(otherManifests) > 0 {
		logger.Debug("Found %d non-CRD resources in CRDs helmfile output, ignoring them", len(otherManifests))
	}

	// Apply only the CRDs
	if len(crdManifests) > 0 {
		logger.Info("Applying %d CRDs...", len(crdManifests))
		crdYaml := strings.Join(crdManifests, "\n---\n")

		if applyOutput, err := bootstrapKubectlCombinedIn(config, bytes.NewReader([]byte(crdYaml)), "apply", "--server-side", "--filename", "-"); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w\nOutput: %s", err, redactCommandOutput(applyOutput))
		}

		logger.Info("CRDs applied, waiting for them to be established...")

		// Wait for CRDs to be established
		if err := bootstrapWaitCRDs(config, logger); err != nil {
			return fmt.Errorf("CRDs failed to be established: %w", err)
		}
	} else {
		logger.Warn("No CRDs found in helmfile template output")
	}

	logger.Success("CRDs applied and established successfully")
	return nil
}

// separateCRDsFromManifests separates CRD manifests from other manifests
func separateCRDsFromManifests(manifestsYaml string) ([]string, []string, error) {
	var crdManifests []string
	var otherManifests []string

	// Split by YAML document separator
	documents := strings.Split(manifestsYaml, "\n---\n")

	for _, doc := range documents {
		doc = strings.TrimSpace(doc)
		if doc == "" || doc == "---" {
			continue
		}

		// Check if this is a CRD by looking for "kind: CustomResourceDefinition"
		if strings.Contains(doc, "kind: CustomResourceDefinition") {
			crdManifests = append(crdManifests, doc)
		} else {
			otherManifests = append(otherManifests, doc)
		}
	}

	return crdManifests, otherManifests, nil
}

// waitForCRDsEstablished waits for all CRDs to be established using progress-based detection
// It keeps waiting as long as progress is being made, only failing if stuck for too long
func waitForCRDsEstablished(config *BootstrapConfig, logger *common.ColorLogger) error {
	checkInterval := bootstrapCheckIntervalFast
	stallTimeout := bootstrapStallTimeout
	maxWait := bootstrapCRDMaxWait

	startTime := bootstrapNow()
	lastProgressTime := bootstrapNow()
	lastEstablishedCount := 0

	for {
		elapsed := bootstrapNow().Sub(startTime)

		// Safety net: fail if we've been waiting too long overall
		if elapsed > maxWait {
			return fmt.Errorf("CRDs did not become established after %v (max wait exceeded)", elapsed.Round(time.Second))
		}

		output, err := bootstrapKubectlOutput(config, "get", "crd",
			"--output=jsonpath={range .items[*]}{.metadata.name}:{.status.conditions[?(@.type=='Established')].status}{\"\\n\"}{end}")
		if err != nil {
			logger.Debug("Failed to check CRD status: %v", err)
			bootstrapSleep(checkInterval)
			continue
		}

		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		allEstablished := true
		establishedCount := 0
		totalCRDs := 0
		var pendingCRDs []string

		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) == 2 {
				totalCRDs++
				crdName := parts[0]
				status := parts[1]
				if status == "True" {
					establishedCount++
				} else {
					allEstablished = false
					pendingCRDs = append(pendingCRDs, crdName)
				}
			}
		}

		// Success: all CRDs established
		if allEstablished && establishedCount > 0 {
			logger.Success("All %d CRDs are established (took %v)", establishedCount, elapsed.Round(time.Second))
			return nil
		}

		// Check for progress
		if establishedCount > lastEstablishedCount {
			logger.Debug("Progress: %d/%d CRDs established (+%d)", establishedCount, totalCRDs, establishedCount-lastEstablishedCount)
			lastProgressTime = bootstrapNow()
			lastEstablishedCount = establishedCount
		}

		// Check for stall
		stallDuration := bootstrapNow().Sub(lastProgressTime)
		if stallDuration > stallTimeout {
			return fmt.Errorf("CRD establishment stalled: no progress for %v (stuck at %d/%d). Pending: %v",
				stallDuration.Round(time.Second), establishedCount, totalCRDs, pendingCRDs)
		}

		// Periodic status update
		if int(elapsed.Seconds())%20 == 0 && elapsed.Seconds() > 0 {
			logger.Info("Waiting for CRDs: %d/%d established, %v elapsed", establishedCount, totalCRDs, elapsed.Round(time.Second))
			if len(pendingCRDs) > 0 && len(pendingCRDs) <= 5 {
				logger.Debug("Pending CRDs: %v", pendingCRDs)
			}
		}

		bootstrapSleep(checkInterval)
	}
}

// fixExistingCRDMetadata adds Helm ownership metadata to existing CRDs that lack it
// This allows Helm to adopt CRDs that were previously installed manually or by other means
func fixExistingCRDMetadata(config *BootstrapConfig, logger *common.ColorLogger) error {
	logger.Info("Checking for CRDs that need Helm ownership metadata...")

	// Define known CRD groups and their Helm release ownership
	// Only include CRD groups that are actually managed by Helm releases.
	// Gateway API CRDs (gateway.networking.k8s.io) are installed via Kustomize
	// from the official kubernetes-sigs/gateway-api GitHub release, not Helm.
	crdGroups := map[string]struct {
		releaseName      string
		releaseNamespace string
	}{
		"external-secrets.io": {releaseName: "external-secrets", releaseNamespace: constants.NSExternalSecret},
		"cert-manager.io":     {releaseName: "cert-manager", releaseNamespace: constants.NSCertManager},
	}

	// Get all CRDs
	output, err := bootstrapKubectlOutput(config, "get", "crds", "-o", "json")
	if err != nil {
		return fmt.Errorf("failed to get CRDs: %w", err)
	}

	// Parse the JSON output
	var crdList struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}

	if err := json.Unmarshal(output, &crdList); err != nil {
		return fmt.Errorf("failed to parse CRD list: %w", err)
	}

	fixedCount := 0
	for _, crd := range crdList.Items {
		// Check if CRD already has Helm ownership metadata
		if crd.Metadata.Labels["app.kubernetes.io/managed-by"] == "Helm" &&
			crd.Metadata.Annotations["meta.helm.sh/release-name"] != "" {
			continue
		}

		// Determine which Helm release should own this CRD based on its group
		var owner *struct {
			releaseName      string
			releaseNamespace string
		}

		for groupSuffix, groupOwner := range crdGroups {
			if strings.HasSuffix(crd.Metadata.Name, groupSuffix) {
				owner = &groupOwner
				break
			}
		}

		// If we don't know the owner, skip this CRD
		if owner == nil {
			continue
		}

		logger.Debug("Adding Helm metadata to CRD: %s (owner: %s/%s)",
			crd.Metadata.Name, owner.releaseNamespace, owner.releaseName)

		// Patch the CRD with Helm ownership metadata
		if output, err := bootstrapKubectlCombined(config, "annotate", "crd", crd.Metadata.Name,
			fmt.Sprintf("meta.helm.sh/release-name=%s", owner.releaseName),
			fmt.Sprintf("meta.helm.sh/release-namespace=%s", owner.releaseNamespace),
			"--overwrite"); err != nil {
			logger.Warn("Failed to add annotations to CRD %s: %v\nOutput: %s",
				crd.Metadata.Name, err, redactCommandOutput(output))
			continue
		}

		if output, err := bootstrapKubectlCombined(config, "label", "crd", crd.Metadata.Name,
			"app.kubernetes.io/managed-by=Helm",
			"--overwrite"); err != nil {
			logger.Warn("Failed to add labels to CRD %s: %v\nOutput: %s",
				crd.Metadata.Name, err, redactCommandOutput(output))
			continue
		}

		fixedCount++
	}

	if fixedCount > 0 {
		logger.Success("Added Helm ownership metadata to %d CRDs", fixedCount)
	} else {
		logger.Info("All CRDs already have proper Helm ownership metadata")
	}

	return nil
}
