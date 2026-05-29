package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
)

// This file adds an end-to-end kubeadm bootstrap path for the Talos->Flatcar
// migration. It is purely ADDITIVE: it reuses the generic post-CNI steps from
// bootstrap.go (waitForNodes, applyNamespaces, applyResources, applyCRDs,
// syncHelmReleases, waitForFluxReconciliation) via their existing func vars and
// only replaces the Talos-specific provisioning steps (apply-config + talos
// bootstrap + talosctl kubeconfig fetch) with kubeadm-over-SSH equivalents.
//
// It is wired into the existing `bootstrap` command via `--provider flatcar`
// (see NewCommand / runBootstrap in bootstrap.go). The flow lives in the
// bootstrap package so it can call the unexported reuse helpers directly rather
// than exporting a large surface or duplicating logic.

// flatcarBootstrapNode carries the per-node identity for the kubeadm flow.
type flatcarBootstrapNode struct {
	Name string
	IP   string
}

// Swappable function vars for the Flatcar kubeadm path. Tests inject fakes for
// these so no real SSH / 1Password / kubectl is touched.
var (
	// flatcarGetVersions resolves K8s/kube-vip/pause versions (reuses the same
	// loader the rest of the CLI uses).
	flatcarGetVersions = versionconfig.GetVersions

	// flatcarGetSSHUser resolves the node SSH username from 1Password. The SSH
	// private key is passed to the Orchestrator as an op:// item ref (resolved
	// lazily by the SSH client), so we only need the username here.
	flatcarGetSSHUser = func() (string, error) {
		return common.Get1PasswordSecret(constants.OpFlatcarSSHUser)
	}

	// flatcarNewOrchestrator builds the kubeadm SSH orchestrator. Swappable so
	// tests can supply a fake that records init/join calls.
	flatcarNewOrchestrator = func(sshUser string) flatcarOrchestrator {
		return flatcar.NewOrchestrator(flatcar.OrchestratorConfig{
			SSHUser:    sshUser,
			SSHItemRef: constants.OpFlatcarSSHPrivateKey,
		})
	}

	// flatcarRenderKubeadmInit / flatcarRenderKubeadmJoin render the kubeadm
	// configs from the embedded templates.
	flatcarRenderKubeadmInit = flatcar.RenderKubeadmInitConfig
	flatcarRenderKubeadmJoin = flatcar.RenderKubeadmJoinConfig

	// flatcarGetNodeConfig resolves a predefined Flatcar node (name -> IP).
	flatcarGetNodeConfig = proxmox.GetFlatcarNodeConfig

	// flatcarPreflight runs the Flatcar-specific preflight checks.
	flatcarPreflight = runFlatcarPreflight

	// flatcarCheckNode verifies a single node is reachable over SSH, booted into
	// Flatcar and has kubelet present. Swappable so tests don't hit real SSH.
	flatcarCheckNode = checkFlatcarNodeReady

	// flatcarInstallCilium installs the Cilium CNI release ONLY (before Flux), so
	// nodes go Ready and the VIP/LB plane comes up. Reuses the bootstrap helmfile.
	flatcarInstallCilium = installCiliumOnly

	// flatcarRunHelmfileSelectorSyncCmd runs helmfile sync limited to a selector.
	// Mirrors bootstrapRunHelmfileSyncCmd but adds --selector.
	flatcarRunHelmfileSelectorSyncCmd = func(tempDir, helmfilePath, selector string, config *BootstrapConfig) error {
		cmd := buildHelmfileCmd(tempDir, config, "--file", helmfilePath, "--selector", selector, "sync", "--hide-notes")
		cmd.Stdout = bootstrapHelmfileStdout
		cmd.Stderr = bootstrapHelmfileStderr
		cmd.Env = append(cmd.Env, fmt.Sprintf("HELMFILE_TEMPLATE_DIR=%s", tempDir))
		return cmd.Run()
	}
)

// flatcarOrchestrator is the subset of *flatcar.Orchestrator the bootstrap flow
// needs. Declaring it here lets tests substitute a fake.
type flatcarOrchestrator interface {
	InitFirstControlPlane(node0IP, initConfig string, skipPhases []string) (*flatcar.KubeadmResult, error)
	JoinControlPlane(nodeIP, joinConfig string) error
	FetchAdminKubeconfig(node0IP string) (string, error)
}

// flatcarNodes returns the ordered control-plane nodes (k8s-0 = init node).
func flatcarNodes() ([]flatcarBootstrapNode, error) {
	names := []string{"k8s-0", "k8s-1", "k8s-2"}
	nodes := make([]flatcarBootstrapNode, 0, len(names))
	for _, name := range names {
		cfg, ok := flatcarGetNodeConfig(name)
		if !ok {
			return nil, fmt.Errorf("unknown flatcar node %q", name)
		}
		nodes = append(nodes, flatcarBootstrapNode{Name: cfg.Name, IP: cfg.NodeIP})
	}
	return nodes, nil
}

// buildFlatcarNodeEnv assembles the per-node template env for the kubeadm flow.
func buildFlatcarNodeEnv(node flatcarBootstrapNode, versions *versionconfig.VersionConfig) flatcar.NodeEnv {
	return flatcar.NodeEnv{
		NodeName:          node.Name,
		NodeIP:            node.IP,
		Node0IP:           constants.FlatcarNode0IP,
		Node1IP:           constants.FlatcarNode1IP,
		Node2IP:           constants.FlatcarNode2IP,
		KubernetesVersion: versions.KubernetesVersion,
		KubernetesMinor:   kubernetesMinor(versions.KubernetesVersion),
		ControlPlaneVIP:   constants.DefaultControlPlaneVIP,
		PauseImage:        versions.PauseImage,
		KubeVipVersion:    versions.KubeVipVersion,
		NodeInterface:     constants.DefaultNodeInterface,
	}
}

// kubernetesMinor derives "vX.Y" from "vX.Y.Z" (local copy; the flatcar cmd has
// its own, but cross-package use would create a cycle).
func kubernetesMinor(version string) string {
	for i := 0; i < len(version); i++ {
		if version[i] == '.' {
			for j := i + 1; j < len(version); j++ {
				if version[j] == '.' {
					return version[:j]
				}
			}
		}
	}
	return version
}

// runBootstrapFlatcar executes the Flatcar/kubeadm bootstrap in order:
//
//	preflight -> init node0 -> fetch+save kubeconfig -> join node1/node2
//	-> install Cilium (CNI) -> [generic] waitForNodes -> applyNamespaces
//	-> applyResources -> applyCRDs -> syncHelmReleases -> waitForFlux
//
// Steps after Cilium reuse the existing bootstrap.go func vars unchanged.
func runBootstrapFlatcar(config *BootstrapConfig) error {
	logger := common.NewColorLogger()
	logger.Info("🚀 Starting Flatcar/kubeadm cluster bootstrap process")

	if !filepath.IsAbs(config.RootDir) {
		absPath, err := filepath.Abs(config.RootDir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for root directory: %w", err)
		}
		config.RootDir = absPath
	}

	// Resolve versions (K8s / kube-vip / pause) once for the whole flow.
	versions := flatcarGetVersions(config.RootDir)
	if config.K8sVersion == "" {
		config.K8sVersion = versions.KubernetesVersion
	}
	logger.Debug("Flatcar bootstrap versions: K8s=%s kube-vip=%s pause=%s",
		versions.KubernetesVersion, versions.KubeVipVersion, versions.PauseImage)

	nodes, err := flatcarNodes()
	if err != nil {
		return err
	}

	// Step 0: Preflight (tools + node reachability + kubelet present).
	if !config.SkipPreflight {
		if err := bootstrapRunWithSpinner("🔍 Running Flatcar preflight checks", config.Verbose, logger, func() error {
			return flatcarPreflight(config, nodes, logger)
		}); err != nil {
			return fmt.Errorf("flatcar preflight checks failed: %w", err)
		}
	} else {
		logger.Warn("⚠️  Skipping preflight checks")
	}

	// Resolve SSH user + build the orchestrator (the private key is an op:// ref
	// resolved lazily by the SSH client).
	sshUser, err := flatcarGetSSHUser()
	if err != nil {
		return fmt.Errorf("failed to resolve Flatcar SSH user from 1Password: %w", err)
	}
	orch := flatcarNewOrchestrator(sshUser)

	node0 := nodes[0]

	// Step 1: Init first control-plane (node0).
	var kubeadmResult *flatcar.KubeadmResult
	if err := bootstrapRunWithSpinner(fmt.Sprintf("🎯 Step 1: kubeadm init on %s (%s)", node0.Name, node0.IP), config.Verbose, logger, func() error {
		if config.DryRun {
			logger.Info("[DRY RUN] Would render kubeadm init config and run kubeadm init on %s", node0.IP)
			kubeadmResult = &flatcar.KubeadmResult{}
			return nil
		}
		initEnv := buildFlatcarNodeEnv(node0, versions)
		initConfig, rErr := flatcarRenderKubeadmInit(initEnv)
		if rErr != nil {
			return fmt.Errorf("failed to render kubeadm init config: %w", rErr)
		}
		res, iErr := orch.InitFirstControlPlane(node0.IP, initConfig, nil)
		if iErr != nil {
			return fmt.Errorf("kubeadm init failed: %w", iErr)
		}
		if res == nil || res.BootstrapToken == "" || res.CACertHash == "" || res.CertificateKey == "" {
			return fmt.Errorf("kubeadm init did not return complete join material (token/ca-hash/cert-key)")
		}
		kubeadmResult = res
		return nil
	}); err != nil {
		return err
	}

	// Step 2: Fetch + save + validate kubeconfig (reuse 1Password save +
	// validate helpers from bootstrap.go).
	if err := bootstrapRunWithSpinner("🔑 Step 2: Fetching and validating kubeconfig", config.Verbose, logger, func() error {
		return fetchFlatcarKubeconfig(config, orch, node0, logger)
	}); err != nil {
		return err
	}

	// Step 3: Join the remaining control-plane nodes (via the VIP).
	for _, node := range nodes[1:] {
		node := node
		if err := bootstrapRunWithSpinner(fmt.Sprintf("➕ Step 3: kubeadm join %s (%s) as control-plane", node.Name, node.IP), config.Verbose, logger, func() error {
			if config.DryRun {
				logger.Info("[DRY RUN] Would render kubeadm join config and join %s via VIP %s", node.IP, constants.DefaultControlPlaneVIP)
				return nil
			}
			joinEnv := buildFlatcarNodeEnv(node, versions)
			joinEnv.BootstrapToken = kubeadmResult.BootstrapToken
			joinEnv.CACertHash = kubeadmResult.CACertHash
			joinEnv.CertificateKey = kubeadmResult.CertificateKey
			joinConfig, rErr := flatcarRenderKubeadmJoin(joinEnv)
			if rErr != nil {
				return fmt.Errorf("failed to render kubeadm join config for %s: %w", node.Name, rErr)
			}
			if jErr := orch.JoinControlPlane(node.IP, joinConfig); jErr != nil {
				return fmt.Errorf("kubeadm join failed for %s: %w", node.Name, jErr)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	logger.Info("Nodes will report NotReady until the CNI is installed (expected)")

	// Step 4: Install Cilium (CNI) BEFORE Flux so nodes go Ready and the
	// VIP/LoadBalancer plane works.
	if err := bootstrapRunWithSpinner("🕸️  Step 4: Installing Cilium CNI", config.Verbose, logger, func() error {
		return flatcarInstallCilium(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to install Cilium: %w", err)
	}

	// Step 5: Wait for nodes to be ready (generic; reused).
	if err := bootstrapRunWithSpinner("⏳ Step 5: Waiting for nodes to be ready", config.Verbose, logger, func() error {
		return bootstrapWaitForNodes(config, logger)
	}); err != nil {
		return fmt.Errorf("failed waiting for nodes: %w", err)
	}

	// Step 6: Namespaces (generic; reused).
	if err := bootstrapRunWithSpinner("📦 Step 6: Creating initial namespaces", config.Verbose, logger, func() error {
		return bootstrapApplyNamespaces(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to apply namespaces: %w", err)
	}

	// Step 7: Initial resources (generic; reused).
	if !config.SkipResources {
		if err := bootstrapRunWithSpinner("🔧 Step 7: Applying initial resources", config.Verbose, logger, func() error {
			return bootstrapApplyResources(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply resources: %w", err)
		}
	}

	// Step 8: CRDs (generic; reused).
	if !config.SkipCRDs {
		if err := bootstrapRunWithSpinner("📜 Step 8: Applying Custom Resource Definitions", config.Verbose, logger, func() error {
			return bootstrapApplyCRDs(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w", err)
		}
	}

	// Step 9: Helm releases via helmfile (generic; reused). This installs the
	// remaining stack (coredns, spegel, cert-manager, external-secrets, flux).
	if !config.SkipHelmfile {
		if err := bootstrapRunWithSpinner("⚙️  Step 9: Syncing Helm releases", config.Verbose, logger, func() error {
			return bootstrapSyncHelmReleases(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to sync Helm releases: %w", err)
		}
	}

	// Step 10: Wait for Flux initial reconciliation (generic; reused).
	if !config.SkipHelmfile {
		if err := bootstrapRunWithSpinner("🔄 Step 10: Waiting for Flux initial reconciliation", config.Verbose, logger, func() error {
			return bootstrapWaitForFlux(config, logger)
		}); err != nil {
			logger.Warn("Flux reconciliation wait completed with warnings: %v", err)
			logger.Info("Cluster is functional but Flux may still be reconciling in the background")
		}
	}

	logger.Success("🎉 Flatcar/kubeadm cluster bootstrapped and Flux reconciliation initiated")
	return nil
}

// fetchFlatcarKubeconfig fetches admin.conf from node0 over SSH, writes it
// locally, saves it to 1Password and validates it. Reuses the existing
// bootstrapSaveKubeconfig + bootstrapValidateKubeconfig func vars. The external
// kubeconfig server points at the control-plane VIP / k8s endpoint so the saved
// config keeps working once the VIP is up.
func fetchFlatcarKubeconfig(config *BootstrapConfig, orch flatcarOrchestrator, node0 flatcarBootstrapNode, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would fetch admin.conf from %s and save to 1Password", node0.IP)
		return nil
	}

	kubeconfig, err := orch.FetchAdminKubeconfig(node0.IP)
	if err != nil {
		return fmt.Errorf("failed to fetch kubeconfig from %s: %w", node0.IP, err)
	}

	// Ensure the kubeconfig directory exists, then write it locally.
	if config.KubeConfig != "" {
		if mkErr := os.MkdirAll(filepath.Dir(config.KubeConfig), 0o755); mkErr != nil {
			return fmt.Errorf("failed to create kubeconfig directory: %w", mkErr)
		}
		if wErr := os.WriteFile(config.KubeConfig, []byte(kubeconfig), 0o600); wErr != nil {
			return fmt.Errorf("failed to write kubeconfig to %s: %w", config.KubeConfig, wErr)
		}
	}

	// Save to 1Password (reused helper). Non-fatal on failure.
	if err := bootstrapSaveKubeconfig([]byte(kubeconfig), logger); err != nil {
		logger.Warn("Failed to save kubeconfig to 1Password: %v", err)
		logger.Warn("Continuing with bootstrap - kubeconfig is available locally")
	} else {
		logger.Success("Kubeconfig saved to 1Password")
	}

	// During bootstrap the VIP is not live until kube-vip + Cilium are up, so
	// point the local kubeconfig at node0 directly. Reuse the existing patch
	// helper. The external/1Password copy keeps admin.conf's server (the VIP /
	// k8s endpoint) untouched on purpose.
	if config.KubeConfig != "" {
		if err := bootstrapPatchKubeconfig(config.KubeConfig, node0.IP, logger); err != nil {
			logger.Warn("Failed to patch kubeconfig for bootstrap: %v", err)
		}
	}

	if err := bootstrapValidateKubeconfig(config, logger); err != nil {
		return fmt.Errorf("kubeconfig validation failed: %w", err)
	}
	logger.Success("Kubeconfig fetched and validated successfully")
	return nil
}

// installCiliumOnly stages the bootstrap helmfile (same machinery as
// executeHelmfileSync) and runs `helmfile sync --selector name=cilium`, so only
// the CNI release is installed before the full stack / Flux. This keeps a single
// source of truth for the Cilium chart version + values.
func installCiliumOnly(config *BootstrapConfig, logger *common.ColorLogger) error {
	if config.DryRun {
		logger.Info("[DRY RUN] Would install Cilium via helmfile (selector name=cilium)")
		return nil
	}

	tempDir, err := os.MkdirTemp("", "homeops-flatcar-cilium-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tempDir); removeErr != nil {
			logger.Warn("Warning: failed to remove temp directory: %v", removeErr)
		}
	}()

	// Stage values template (templates/values.yaml.gotmpl) — identical layout to
	// executeHelmfileSync so the helmfile's ./templates/values.yaml.gotmpl ref
	// resolves.
	templatesDir := filepath.Join(tempDir, "templates")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create templates directory: %w", err)
	}
	valuesTemplate, err := bootstrapGetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return fmt.Errorf("failed to get values template: %w", err)
	}
	if err := os.WriteFile(filepath.Join(templatesDir, "values.yaml.gotmpl"), []byte(valuesTemplate), 0o644); err != nil {
		return fmt.Errorf("failed to write values template: %w", err)
	}

	appsHelmfile, err := bootstrapGetBootstrapFile("helmfile.d/01-apps.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded apps helmfile: %w", err)
	}
	helmfilePath := filepath.Join(tempDir, "01-apps.yaml")
	if err := os.WriteFile(helmfilePath, []byte(appsHelmfile), 0o644); err != nil {
		return fmt.Errorf("failed to write apps helmfile: %w", err)
	}

	// Retry the Cilium-only sync with backoff, reusing the retry classifier.
	maxAttempts := 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			logger.Info("Cilium install attempt %d/%d", attempt, maxAttempts)
		}
		err := flatcarRunHelmfileSelectorSyncCmd(tempDir, helmfilePath, "name=cilium", config)
		if err == nil {
			logger.Success("Cilium CNI installed")
			return nil
		}
		lastErr = err
		if !isRetryableHelmError(err) {
			return fmt.Errorf("cilium install failed: %w", err)
		}
		logger.Warn("Cilium install attempt %d/%d failed (retryable): %v", attempt, maxAttempts, err)
		if attempt < maxAttempts {
			bootstrapSleep(time.Duration(attempt*30) * time.Second)
		}
	}
	return fmt.Errorf("cilium install failed after %d attempts: %w", maxAttempts, lastErr)
}

// runFlatcarPreflight validates the Flatcar/kubeadm prerequisites: required
// local tools, SSH reachability to all 3 nodes, and that each node is booted
// into Flatcar with kubelet present.
func runFlatcarPreflight(config *BootstrapConfig, nodes []flatcarBootstrapNode, logger *common.ColorLogger) error {
	// 1. Local tools needed for the post-CNI generic steps.
	requiredBins := []string{"kubectl", "helmfile", "op"}
	var missing []string
	for _, bin := range requiredBins {
		if _, err := bootstrapLookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required tools: %v", missing)
	}

	// 2. 1Password auth (needed to resolve SSH creds + save kubeconfig).
	if err := bootstrapEnsureOPAuth(); err != nil {
		return fmt.Errorf("1Password authentication failed: %w", err)
	}

	if config.DryRun {
		logger.Info("[DRY RUN] Would verify SSH reachability + kubelet on %d Flatcar nodes", len(nodes))
		return nil
	}

	// 3. SSH reachability + Flatcar/kubelet presence on each node.
	sshUser, err := flatcarGetSSHUser()
	if err != nil {
		return fmt.Errorf("failed to resolve Flatcar SSH user: %w", err)
	}
	for _, node := range nodes {
		if err := flatcarCheckNode(sshUser, node, logger); err != nil {
			return fmt.Errorf("node %s (%s) preflight failed: %w", node.Name, node.IP, err)
		}
		logger.Debug("Node %s (%s) reachable, Flatcar booted, kubelet present", node.Name, node.IP)
	}
	return nil
}

// flatcarNewSSHRunner builds an SSH runner for a node (swappable for tests).
var flatcarNewSSHRunner = func(sshUser, host string) flatcarSSHRunner {
	return ssh.NewSSHClient(ssh.SSHConfig{
		Host:       host,
		Username:   sshUser,
		Port:       "22",
		SSHItemRef: constants.OpFlatcarSSHPrivateKey,
	})
}

// flatcarSSHRunner is the minimal SSH surface the node preflight needs.
type flatcarSSHRunner interface {
	Connect() error
	Close() error
	ExecuteCommand(command string) (string, error)
}

// checkFlatcarNodeReady SSHes to a node and verifies it is booted into Flatcar
// (os-release ID=flatcar) and that the kubelet binary is present, so the kubeadm
// init/join steps have what they need.
func checkFlatcarNodeReady(sshUser string, node flatcarBootstrapNode, logger *common.ColorLogger) error {
	runner := flatcarNewSSHRunner(sshUser, node.IP)
	if err := runner.Connect(); err != nil {
		return fmt.Errorf("ssh connect failed: %w", err)
	}
	defer func() { _ = runner.Close() }()

	// Verify Flatcar + kubelet in one round trip.
	out, err := runner.ExecuteCommand("grep -q '^ID=flatcar' /etc/os-release && command -v kubelet")
	if err != nil {
		return fmt.Errorf("node not booted into Flatcar or kubelet missing: %w", err)
	}
	if !strings.Contains(out, "kubelet") {
		return fmt.Errorf("kubelet not found on node (output: %q)", strings.TrimSpace(out))
	}
	return nil
}
