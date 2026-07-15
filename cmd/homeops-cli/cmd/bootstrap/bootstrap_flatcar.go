package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/flatcar"
	"homeops-cli/internal/proxmox"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/ui"
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

	// flatcarGetSSHUser resolves the node SSH username through the configured
	// secret reference (secrets.node_ssh_user; defaults to literal://core).
	// SSH key auth itself is the ambient ssh-agent's job.
	flatcarGetSSHUser = func() (string, error) {
		return versionconfig.Get().ResolveSecret(versionconfig.KeyNodeSSHUser)
	}

	// flatcarGetK8sEndpoint resolves the apiserver certSAN DNS name from the
	// homeops config (explicit cluster.endpoint, or "k8s." + the resolved
	// cluster domain). Swappable so tests don't touch secret backends.
	flatcarGetK8sEndpoint = func() string {
		return versionconfig.Get().APIEndpoint()
	}

	// flatcarFreshPKI mirrors BootstrapConfig.FreshPKI into the orchestrator
	// factory (set at the top of runBootstrapFlatcar). When false (default), the
	// orchestrator restores the persisted cluster PKI from 1Password before
	// `kubeadm init`; when true (--fresh-pki), kubeadm mints a new CA.
	flatcarFreshPKI = false

	// flatcarNewOrchestrator builds the kubeadm SSH orchestrator. Swappable so
	// tests can supply a fake that records init/join calls.
	flatcarNewOrchestrator = func(sshUser string) flatcarOrchestrator {
		return flatcar.NewOrchestrator(flatcar.OrchestratorConfig{
			SSHUser:  sshUser,
			FreshPKI: flatcarFreshPKI,
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

// flatcarNodes returns the ordered control-plane nodes (the first configured
// node is the kubeadm init node).
func flatcarNodes() ([]flatcarBootstrapNode, error) {
	names := versionconfig.Get().NodeNames()
	if len(names) == 0 {
		return nil, fmt.Errorf("no cluster nodes configured (cluster.nodes in homeops.yaml)")
	}
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
	cfg := versionconfig.Get()
	clusterNodes := cfg.Cluster.Nodes
	nodeIP := func(i int) string {
		if i < len(clusterNodes) {
			return clusterNodes[i].IP
		}
		return ""
	}
	return flatcar.NodeEnv{
		NodeName:          node.Name,
		NodeIP:            node.IP,
		Node0IP:           nodeIP(0),
		Node1IP:           nodeIP(1),
		Node2IP:           nodeIP(2),
		KubernetesVersion: versions.KubernetesVersion,
		KubernetesMinor:   flatcar.KubernetesMinor(versions.KubernetesVersion),
		ControlPlaneVIP:   cfg.Cluster.ControlPlaneVIP,
		PauseImage:        versions.PauseImage,
		KubeVipVersion:    versions.KubeVipVersion,
		NodeInterface:     cfg.Cluster.NodeInterface,
		K8sEndpoint:       flatcarGetK8sEndpoint(),
		ClusterName:       cfg.ClusterNameWithDefault(),
		PodCIDR:           cfg.Cluster.PodCIDR,
		ServiceCIDR:       cfg.Cluster.ServiceCIDR,
		DNSDomain:         cfg.Cluster.DNSDomain,
		ClusterDNS:        cfg.ClusterDNS(),
	}
}

// bootstrapStepper numbers the steps that will actually run (skips excluded)
// so progress reads "Step 3/7" instead of a fixed count that lies under
// --skip-* flags.
type bootstrapStepper struct {
	current, total int
}

// flatcarStepTotal counts the steps the configured flags will execute.
func flatcarStepTotal(config *BootstrapConfig) int {
	total := 10 // init, kubeconfig, join, cilium, nodes, namespaces, resources, crds, helmfile, flux
	if config.SkipKubeadm {
		total -= 3 // init, kubeconfig, join
	}
	if config.SkipResources {
		total--
	}
	if config.SkipCRDs {
		total--
	}
	if config.SkipHelmfile {
		total -= 2 // helmfile sync + flux wait
	}
	return total
}

// next returns the spinner title for the next step.
func (s *bootstrapStepper) next(emoji, text string) string {
	s.current++
	return fmt.Sprintf("%s Step %d/%d: %s", emoji, s.current, s.total, text)
}

// sub keeps the current step number for repeated work inside one step
// (e.g. joining each remaining control-plane node).
func (s *bootstrapStepper) sub(emoji, text string) string {
	return fmt.Sprintf("%s Step %d/%d: %s", emoji, s.current, s.total, text)
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

	// Default: restore the persisted cluster PKI before kubeadm init (stable
	// identity across rebuilds). --fresh-pki opts out and mints a new CA.
	flatcarFreshPKI = config.FreshPKI

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

	// Plan panel (TTY only; the Info lines above cover CI logs).
	nodeDescs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		nodeDescs = append(nodeDescs, fmt.Sprintf("%s (%s)", n.Name, n.IP))
	}
	mode := "apply"
	if config.DryRun {
		mode = "DRY RUN — no changes will be made"
	}
	ui.PrintInfoBox("Flatcar/kubeadm bootstrap",
		"nodes:    "+strings.Join(nodeDescs, ", "),
		fmt.Sprintf("k8s:      %s   kube-vip: %s", versions.KubernetesVersion, versions.KubeVipVersion),
		"VIP:      "+versionconfig.Get().Cluster.ControlPlaneVIP,
		"mode:     "+mode)
	if config.DryRun {
		logger.Info("DRY RUN: every step below only describes what it would do")
	}

	steps := &bootstrapStepper{total: flatcarStepTotal(config)}

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
	if config.SkipKubeadm {
		logger.Warn("⚠️  Skipping kubeadm init/join (--skip-kubeadm): using existing control plane")
	}
	if !config.SkipKubeadm {
		if err := bootstrapRunWithSpinner(steps.next("🎯", fmt.Sprintf("kubeadm init on %s (%s)", node0.Name, node0.IP)), config.Verbose, logger, func() error {
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
	} // end if !SkipKubeadm (step 1 init)

	// Step 2: Fetch + save + validate kubeconfig (reuse 1Password save +
	// validate helpers from bootstrap.go). Skipped with --skip-kubeadm; the
	// caller must pass --kubeconfig for an already-built control plane.
	if !config.SkipKubeadm {
		if err := bootstrapRunWithSpinner(steps.next("🔑", "Fetching and validating kubeconfig"), config.Verbose, logger, func() error {
			return fetchFlatcarKubeconfig(config, orch, node0, logger)
		}); err != nil {
			return err
		}
	} else {
		logger.Info("Using provided --kubeconfig: %s", config.KubeConfig)
	}

	// Step 3: Join the remaining control-plane nodes (via the VIP).
	if !config.SkipKubeadm {
		steps.next("➕", "kubeadm join remaining control-plane nodes")
		for _, node := range nodes[1:] {
			node := node
			if err := bootstrapRunWithSpinner(steps.sub("➕", fmt.Sprintf("kubeadm join %s (%s) as control-plane", node.Name, node.IP)), config.Verbose, logger, func() error {
				if config.DryRun {
					logger.Info("[DRY RUN] Would render kubeadm join config and join %s via VIP %s", node.IP, versionconfig.Get().Cluster.ControlPlaneVIP)
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
	} // end if !SkipKubeadm (step 3 joins)
	logger.Info("Nodes will report NotReady until the CNI is installed (expected)")

	// Step 4: Install Cilium (CNI) BEFORE Flux so nodes go Ready and the
	// VIP/LoadBalancer plane works.
	if err := bootstrapRunWithSpinner(steps.next("🕸️ ", "Installing Cilium CNI"), config.Verbose, logger, func() error {
		return flatcarInstallCilium(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to install Cilium: %w", err)
	}

	// Step 5: Wait for nodes to be ready (generic; reused).
	if err := bootstrapRunWithSpinner(steps.next("⏳", "Waiting for nodes to be ready"), config.Verbose, logger, func() error {
		return bootstrapWaitForNodes(config, logger)
	}); err != nil {
		return fmt.Errorf("failed waiting for nodes: %w", err)
	}

	// Step 6: Namespaces (generic; reused).
	if err := bootstrapRunWithSpinner(steps.next("📦", "Creating initial namespaces"), config.Verbose, logger, func() error {
		return bootstrapApplyNamespaces(config, logger)
	}); err != nil {
		return fmt.Errorf("failed to apply namespaces: %w", err)
	}

	// Step 7: Initial resources (generic; reused).
	if !config.SkipResources {
		if err := bootstrapRunWithSpinner(steps.next("🔧", "Applying initial resources"), config.Verbose, logger, func() error {
			return bootstrapApplyResources(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply resources: %w", err)
		}
	}

	// Step 8: CRDs (generic; reused).
	if !config.SkipCRDs {
		if err := bootstrapRunWithSpinner(steps.next("📜", "Applying Custom Resource Definitions"), config.Verbose, logger, func() error {
			return bootstrapApplyCRDs(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to apply CRDs: %w", err)
		}
	}

	// Step 9: Helm releases via helmfile (generic; reused). This installs the
	// remaining stack (coredns, spegel, cert-manager, external-secrets, flux).
	if !config.SkipHelmfile {
		if err := bootstrapRunWithSpinner(steps.next("⚙️ ", "Syncing Helm releases"), config.Verbose, logger, func() error {
			return bootstrapSyncHelmReleases(config, logger)
		}); err != nil {
			return fmt.Errorf("failed to sync Helm releases: %w", err)
		}
	}

	// Step 10: Wait for Flux initial reconciliation (generic; reused).
	if !config.SkipHelmfile {
		if err := bootstrapRunWithSpinner(steps.next("🔄", "Waiting for Flux initial reconciliation"), config.Verbose, logger, func() error {
			return bootstrapWaitForFlux(config, logger)
		}); err != nil {
			logger.Warn("Flux reconciliation wait completed with warnings: %v", err)
			logger.Info("Cluster is functional but Flux may still be reconciling in the background")
		}
	}

	if config.DryRun {
		logger.Success("✅ Dry run complete — no changes were made (%d step(s) planned)", steps.total)
		ui.PrintInfoBox("Dry run complete",
			fmt.Sprintf("%d step(s) would run against %d node(s).", steps.total, len(nodes)),
			"Re-run without --dry-run to apply.")
		return nil
	}
	logger.Success("🎉 Flatcar/kubeadm cluster bootstrapped and Flux reconciliation initiated")
	ui.PrintSuccessBox("🎉 Cluster bootstrapped!",
		"Flux has completed initial reconciliation.",
		"kubectl get nodes        # say hello to your cluster",
		"flux get kustomizations  # watch the apps roll out")
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
		if mkErr := os.MkdirAll(filepath.Dir(config.KubeConfig), 0o750); mkErr != nil {
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
	if err := os.MkdirAll(templatesDir, 0o750); err != nil {
		return fmt.Errorf("failed to create templates directory: %w", err)
	}
	valuesTemplate, err := bootstrapGetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return fmt.Errorf("failed to get values template: %w", err)
	}
	if err := os.WriteFile(filepath.Join(templatesDir, "values.yaml.gotmpl"), []byte(valuesTemplate), 0o600); err != nil {
		return fmt.Errorf("failed to write values template: %w", err)
	}

	appsHelmfile, err := bootstrapGetBootstrapFile("helmfile.d/01-apps.yaml")
	if err != nil {
		return fmt.Errorf("failed to get embedded apps helmfile: %w", err)
	}
	helmfilePath := filepath.Join(tempDir, "01-apps.yaml")
	if err := os.WriteFile(helmfilePath, []byte(appsHelmfile), 0o600); err != nil {
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
		Host:     host,
		Username: sshUser,
		Port:     "22",
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
		return fmt.Errorf("ssh connect failed: %w (the nodes trust secrets.node_ssh_authorized_key — make sure its PRIVATE key is available to ssh: loaded in your ssh-agent, or an IdentityFile entry for the node IPs in ~/.ssh/config)", err)
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
