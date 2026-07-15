package bootstrap

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	versionconfig "homeops-cli/internal/config"
	"homeops-cli/internal/constants"
	"homeops-cli/internal/secrets"
	"homeops-cli/internal/ui"
)

type bootstrapPlanVersion struct {
	Component string `json:"component"`
	Version   string `json:"version"`
	Source    string `json:"source"`
}

type bootstrapPlanNode struct {
	Order int    `json:"order"`
	Name  string `json:"name"`
	IP    string `json:"ip"`
	Role  string `json:"role"`
}

type bootstrapPlanCheck struct {
	Order  int    `json:"order"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Effect string `json:"effect"`
}

type bootstrapPlanSecret struct {
	Key     string `json:"key"`
	Backend string `json:"backend"`
	Status  string `json:"status"`
}

type bootstrapPlanArtifact struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Effect string `json:"effect"`
}

type bootstrapPlanVM struct {
	Node           string `json:"node"`
	Hypervisor     string `json:"hypervisor"`
	VMID           int    `json:"vmid,omitempty"`
	MAC            string `json:"mac,omitempty"`
	MemoryMB       int    `json:"memory_mb"`
	Cores          int    `json:"cores"`
	BootDiskGB     int    `json:"boot_disk_gb"`
	BootStorage    string `json:"boot_storage"`
	OpenEBSDiskGB  int    `json:"openebs_disk_gb"`
	OpenEBSStorage string `json:"openebs_storage"`
	Ceph           string `json:"ceph"`
	Network        string `json:"network"`
	CPUAffinity    string `json:"cpu_affinity,omitempty"`
	NUMANode       string `json:"numa_node,omitempty"`
}

type bootstrapPlanStep struct {
	Order  int    `json:"order"`
	Action string `json:"action"`
	Effect string `json:"effect"`
	Status string `json:"status"`
}

type bootstrapPlan struct {
	NoChanges    bool                    `json:"no_changes"`
	Cluster      string                  `json:"cluster"`
	Provider     string                  `json:"provider"`
	Hypervisor   string                  `json:"hypervisor"`
	ConfigSource string                  `json:"config_source"`
	Versions     []bootstrapPlanVersion  `json:"versions"`
	Nodes        []bootstrapPlanNode     `json:"nodes"`
	Preflight    []bootstrapPlanCheck    `json:"preflight"`
	Secrets      []bootstrapPlanSecret   `json:"secrets"`
	Artifacts    []bootstrapPlanArtifact `json:"artifacts"`
	VMs          []bootstrapPlanVM       `json:"vm_creation_parameters"`
	JoinSequence []bootstrapPlanStep     `json:"join_sequence"`
	Steps        []bootstrapPlanStep     `json:"steps"`
}

var (
	bootstrapBuildPlanFn       = buildBootstrapPlan
	bootstrapPlanConfigFn      = versionconfig.Get
	bootstrapPlanVersionsFn    = bootstrapGetVersions
	bootstrapPlanSecretCheckFn = func(reference string) bool {
		value, err := secrets.Resolve(reference)
		return err == nil && strings.TrimSpace(value) != ""
	}
)

func buildBootstrapPlan(options BootstrapConfig) (bootstrapPlan, error) {
	cfg := bootstrapPlanConfigFn()
	if err := versionconfig.LoadError(); err != nil {
		return bootstrapPlan{}, fmt.Errorf("load homeops config for bootstrap plan: %w", err)
	}
	provider := strings.ToLower(strings.TrimSpace(options.Provider))
	if provider == "" {
		provider = "flatcar"
	}
	if provider != "flatcar" && provider != "talos" {
		return bootstrapPlan{}, fmt.Errorf("unsupported bootstrap provider %q (flatcar, talos)", options.Provider)
	}
	if len(cfg.Cluster.Nodes) == 0 {
		return bootstrapPlan{}, fmt.Errorf("cluster.nodes has no control-plane nodes")
	}
	for index, node := range cfg.Cluster.Nodes {
		if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.IP) == "" {
			return bootstrapPlan{}, fmt.Errorf("cluster.nodes[%d] requires both name and ip", index)
		}
	}

	versions := bootstrapPlanVersionsFn(options.RootDir)
	if versions == nil {
		return bootstrapPlan{}, fmt.Errorf("resolve bootstrap versions: no version configuration returned")
	}
	kubernetesVersion := options.K8sVersion
	versionSource := "--k8s-version/environment override"
	if kubernetesVersion == "" {
		kubernetesVersion = versions.KubernetesVersion
		versionSource = "kubeadm System Upgrade Controller Plan"
	}

	plan := bootstrapPlan{
		NoChanges:    true,
		Cluster:      cfg.ClusterNameWithDefault(),
		Provider:     provider,
		Hypervisor:   strings.ToLower(cfg.Hypervisors.Default),
		ConfigSource: cfg.Source,
		Versions:     []bootstrapPlanVersion{{Component: "kubernetes", Version: kubernetesVersion, Source: versionSource}},
	}
	if plan.ConfigSource == "" {
		plan.ConfigSource = "built-in defaults"
	}
	if provider == "flatcar" {
		plan.Versions = append(plan.Versions,
			bootstrapPlanVersion{Component: "flatcar", Version: versions.FlatcarVersion, Source: "flatcar-os System Upgrade Controller Plan/default"},
			bootstrapPlanVersion{Component: "kube-vip", Version: versions.KubeVipVersion, Source: "version configuration"},
			bootstrapPlanVersion{Component: "pause image", Version: versions.PauseImage, Source: "version configuration"})
	} else {
		talosVersion := options.TalosVersion
		talosSource := "--talos-version/environment override"
		if talosVersion == "" {
			talosVersion = versions.TalosVersion
			talosSource = "legacy Talos version configuration"
		}
		plan.Versions = append(plan.Versions, bootstrapPlanVersion{Component: "talos", Version: talosVersion, Source: talosSource})
	}

	for index, node := range cfg.Cluster.Nodes {
		role := "control-plane join node"
		if index == 0 {
			role = "control-plane init node"
		}
		plan.Nodes = append(plan.Nodes, bootstrapPlanNode{Order: index + 1, Name: node.Name, IP: node.IP, Role: role})
		plan.VMs = append(plan.VMs, buildBootstrapPlanVM(cfg, node, provider))
	}
	plan.Preflight = buildBootstrapPlanPreflight(provider, options.SkipPreflight, cfg.Cluster.Nodes)
	plan.Artifacts = buildBootstrapPlanArtifacts(provider, options, cfg.Cluster.Nodes)
	plan.Secrets = buildBootstrapPlanSecrets(cfg, provider, options)
	plan.JoinSequence = buildBootstrapJoinSequence(provider, options.SkipKubeadm, cfg.Cluster.Nodes)
	plan.Steps = buildBootstrapPlanSteps(provider, options, cfg.Cluster.Nodes)
	return plan, nil
}

func buildBootstrapPlanPreflight(provider string, skipped bool, nodes []versionconfig.Node) []bootstrapPlanCheck {
	status := "RUN"
	if skipped {
		status = "SKIP (--skip-preflight)"
	}
	var descriptions []struct{ name, effect string }
	if provider == "flatcar" {
		descriptions = []struct{ name, effect string }{
			{"Tool availability", "Require kubectl, helmfile, and op"},
			{"1Password authentication", "Authenticate only when the operational bootstrap runs"},
			{"Flatcar node readiness", fmt.Sprintf("SSH to %d configured node(s); require Flatcar and kubelet", len(nodes))},
		}
	} else {
		descriptions = []struct{ name, effect string }{
			{"Tool Availability", "Require talosctl, kubectl, kustomize, op, and helmfile"},
			{"Environment Files", "Validate version inputs and talosconfig path"},
			{"Network Connectivity", "HEAD github.com for CRD downloads"},
			{"DNS Resolution", "Resolve github.com"},
			{"1Password Authentication", "Authenticate when op:// references are configured"},
			{"Machine Config Rendering", "Render the first Talos machine configuration"},
			{"Talos Nodes", "Read and validate configured Talos node endpoints"},
		}
	}
	checks := make([]bootstrapPlanCheck, 0, len(descriptions))
	for index, description := range descriptions {
		checkStatus := status
		if provider == "talos" && skipped && (description.name == "Tool Availability" || description.name == "Environment Files") {
			checkStatus = "RUN (basic prerequisite fallback)"
		}
		checks = append(checks, bootstrapPlanCheck{Order: index + 1, Name: description.name, Status: checkStatus, Effect: description.effect})
	}
	return checks
}

func buildBootstrapPlanSecrets(cfg *versionconfig.Config, provider string, options BootstrapConfig) []bootstrapPlanSecret {
	keys := map[string]struct{}{}
	addKey := func(key string) {
		if cfg.SecretRef(key) != "" {
			keys[key] = struct{}{}
		}
	}
	addKey(versionconfig.KeyClusterDomain)
	if provider == "flatcar" {
		addKey(versionconfig.KeyNodeSSHUser)
		addKey(versionconfig.KeyNodeSSHAuthorizedKey)
	} else {
		for _, key := range []string{
			versionconfig.KeyTalosMachineCACrt, versionconfig.KeyTalosMachineCAKey, versionconfig.KeyTalosMachineToken,
			versionconfig.KeyTalosClusterCACrt, versionconfig.KeyTalosClusterCAKey, versionconfig.KeyTalosK8sEndpoint,
			versionconfig.KeyTalosClusterID, versionconfig.KeyTalosClusterSecret, versionconfig.KeyTalosClusterToken,
			versionconfig.KeyTalosAggregatorCrt, versionconfig.KeyTalosAggregatorKey, versionconfig.KeyTalosEtcdCACrt,
			versionconfig.KeyTalosEtcdCAKey, versionconfig.KeyTalosSecretboxSecret, versionconfig.KeyTalosSAKey,
		} {
			addKey(key)
		}
	}
	if !options.SkipResources {
		addKey(versionconfig.KeyOpCredentialsJSON)
		addKey(versionconfig.KeyOpConnectToken)
		addKey(versionconfig.KeyCloudflareTunnelID)
	}
	for _, content := range bootstrapPlanSecretTemplateContents(provider, options, cfg.Cluster.Nodes) {
		for _, reference := range secrets.ListReferences(content) {
			if strings.HasPrefix(reference, "secret://") {
				addKey(strings.TrimPrefix(reference, "secret://"))
			}
		}
	}

	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	result := make([]bootstrapPlanSecret, 0, len(ordered))
	for _, key := range ordered {
		reference := cfg.SecretRef(key)
		backend := secretBackend(reference)
		status := "NOT CHECKED"
		if options.CheckSecrets {
			status = "MISSING"
			if bootstrapPlanSecretCheckFn(reference) {
				status = "AVAILABLE"
			}
		}
		result = append(result, bootstrapPlanSecret{Key: key, Backend: backend, Status: status})
	}
	return result
}

func bootstrapPlanSecretTemplateContents(provider string, options BootstrapConfig, nodes []versionconfig.Node) []string {
	var contents []string
	appendIfAvailable := func(content string, err error) {
		if err == nil {
			contents = append(contents, content)
		}
	}
	if !options.SkipResources {
		appendIfAvailable(bootstrapGetBootstrapFile("resources.yaml"))
	}
	if !options.SkipHelmfile {
		appendIfAvailable(bootstrapGetBootstrapFile("clustersecretstore.yaml"))
		appendIfAvailable(bootstrapGetBootstrapTemplate("values.yaml.gotmpl"))
	}
	if provider == "talos" {
		appendIfAvailable(bootstrapGetTalosTemplate("talos/controlplane.yaml"))
		appendIfAvailable(bootstrapGetTalosTemplate("talos/worker.yaml"))
		for _, node := range nodes {
			appendIfAvailable(bootstrapGetTalosTemplate("talos/nodes/" + node.IP + ".yaml"))
		}
	} else {
		for _, name := range []string{"butane/controlplane.bu", "kubeadm/init-config.yaml", "kubeadm/join-config.yaml", "manifests/kube-vip.yaml"} {
			appendIfAvailable(bootstrapGetFlatcarTemplate(name))
		}
	}
	return contents
}

func secretBackend(reference string) string {
	if index := strings.Index(reference, "://"); index > 0 {
		return reference[:index]
	}
	return "unknown"
}

func buildBootstrapPlanArtifacts(provider string, options BootstrapConfig, nodes []versionconfig.Node) []bootstrapPlanArtifact {
	artifacts := []bootstrapPlanArtifact{}
	add := func(name, action, effect string) {
		artifacts = append(artifacts, bootstrapPlanArtifact{Name: name, Action: action, Effect: effect})
	}
	if provider == "flatcar" {
		add("flatcar/butane/controlplane.bu", "render", "Ignition for every configured node (normally staged by flatcar deploy-vm before bootstrap)")
		if !options.SkipKubeadm {
			add("flatcar/kubeadm/init-config.yaml", "render", "kubeadm init configuration for the first node")
			add("flatcar/kubeadm/join-config.yaml", "render", "kubeadm control-plane join configuration for remaining nodes")
		}
		add("flatcar/manifests/kube-vip.yaml", "render", "static kube-vip manifest embedded in Ignition")
	} else {
		add("talos/controlplane.yaml", "render", "base control-plane machine configuration")
		for _, node := range nodes {
			add("talos/nodes/"+node.IP+".yaml", "merge", "node-specific machine configuration for "+node.Name)
		}
	}
	add("bootstrap namespaces", "apply", strings.Join(initialBootstrapNamespaces(), ", "))
	if !options.SkipResources {
		add("bootstrap/resources.yaml", "apply", "initial Secret resources")
	}
	if !options.SkipCRDs {
		add("bootstrap/helmfile.d/00-crds.yaml", "template/apply", "CRDs from the configured Helm releases and Gateway API CRDs")
	}
	if !options.SkipHelmfile {
		add("bootstrap/helmfile.d/templates/values.yaml.gotmpl", "render", "shared Helm values")
		add("bootstrap/helmfile.d/01-apps.yaml", "sync", "cilium, coredns, spegel, cert-manager, external-secrets, flux-operator, flux-instance")
		add("bootstrap/clustersecretstore.yaml", "apply", "ClusterSecretStore/onepassword after External Secrets is ready")
	}
	return artifacts
}

func initialBootstrapNamespaces() []string {
	return []string{
		constants.NSActionsRunner, constants.NSAuth, constants.NSAutomation, constants.NSCertManager, constants.NSDatabase,
		constants.NSDownloads, constants.NSExternalSecret, constants.NSFluxSystem, constants.NSKubeSystem, constants.NSMedia,
		constants.NSNetwork, constants.NSObservability, constants.NSOpenEBSSystem, constants.NSRookCeph, constants.NSSelfHosted,
		constants.NSSystem, constants.NSSystemUpgrade, constants.NSVolsyncSystem,
	}
}

func buildBootstrapPlanVM(cfg *versionconfig.Config, node versionconfig.Node, provider string) bootstrapPlanVM {
	hypervisor := strings.ToLower(cfg.Hypervisors.Default)
	var defaults versionconfig.VMDefaults
	switch hypervisor {
	case "truenas":
		defaults = cfg.Hypervisors.TrueNAS.VM
	case "vsphere":
		defaults = cfg.Hypervisors.VSphere.VM
	default:
		hypervisor = "proxmox"
		defaults = cfg.Hypervisors.Proxmox.VM
	}
	profileProvider := provider
	switch hypervisor {
	case "vsphere":
		profileProvider = "vsphere"
	case "truenas":
		profileProvider = "truenas"
	}
	profile := node.VM.ForProvider(profileProvider)
	bootStorage := firstNonEmpty(profile.BootStorage, defaults.BootStorage)
	openEBSStorage := firstNonEmpty(profile.OpenEBSStorage, defaults.OpenEBSStorage, bootStorage)
	ceph := profile.Ceph
	if ceph.Mode == "" {
		ceph = defaults.Ceph
	}
	cephDescription := firstNonEmpty(ceph.Mode, "default")
	if ceph.DiskByID != "" {
		cephDescription += " by-id=" + ceph.DiskByID
	} else if ceph.SizeGB > 0 {
		cephDescription += fmt.Sprintf(" %dGB on %s", ceph.SizeGB, firstNonEmpty(ceph.Storage, bootStorage))
	}
	numa := "default"
	if profile.NUMANode != nil {
		numa = fmt.Sprintf("%d", *profile.NUMANode)
	}
	network := defaults.NetworkBridge
	if defaults.VLANID > 0 {
		network += fmt.Sprintf(" vlan=%d", defaults.VLANID)
	}
	if defaults.NetworkMTU > 0 {
		network += fmt.Sprintf(" mtu=%d", defaults.NetworkMTU)
	}
	return bootstrapPlanVM{
		Node: node.Name, Hypervisor: hypervisor, VMID: profile.VMID, MAC: profile.Mac,
		MemoryMB: defaults.MemoryMB, Cores: defaults.Cores, BootDiskGB: defaults.BootDiskGB, BootStorage: bootStorage,
		OpenEBSDiskGB: defaults.OpenEBSDiskGB, OpenEBSStorage: openEBSStorage, Ceph: cephDescription,
		Network: network, CPUAffinity: profile.CPUAffinity, NUMANode: numa,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "-"
}

func buildBootstrapJoinSequence(provider string, skipKubeadm bool, nodes []versionconfig.Node) []bootstrapPlanStep {
	if provider == "flatcar" {
		if skipKubeadm {
			return []bootstrapPlanStep{{Order: 1, Action: "Use existing control plane", Effect: "kubeadm init/join and kubeconfig fetch are skipped", Status: "SKIP (--skip-kubeadm)"}}
		}
		steps := []bootstrapPlanStep{{Order: 1, Action: "kubeadm init", Effect: fmt.Sprintf("Initialize %s (%s) and upload control-plane certificates", nodes[0].Name, nodes[0].IP), Status: "RUN"}}
		for index, node := range nodes[1:] {
			steps = append(steps, bootstrapPlanStep{Order: index + 2, Action: "kubeadm join", Effect: fmt.Sprintf("Join %s (%s) as a control-plane node using runtime token/hash/certificate key", node.Name, node.IP), Status: "RUN"})
		}
		return steps
	}
	steps := make([]bootstrapPlanStep, 0, len(nodes)+1)
	for index, node := range nodes {
		steps = append(steps, bootstrapPlanStep{Order: index + 1, Action: "apply Talos machine config", Effect: fmt.Sprintf("Configure %s (%s); the node joins from the shared Talos cluster identity", node.Name, node.IP), Status: "RUN"})
	}
	steps = append(steps, bootstrapPlanStep{Order: len(steps) + 1, Action: "talosctl bootstrap", Effect: "Bootstrap etcd on one configured controller selected at execution, then wait for all nodes", Status: "RUN"})
	return steps
}

func buildBootstrapPlanSteps(provider string, options BootstrapConfig, nodes []versionconfig.Node) []bootstrapPlanStep {
	steps := []bootstrapPlanStep{}
	add := func(action, effect, status string) {
		steps = append(steps, bootstrapPlanStep{Order: len(steps) + 1, Action: action, Effect: effect, Status: status})
	}
	preflightStatus := "RUN"
	if options.SkipPreflight {
		preflightStatus = "SKIP (--skip-preflight)"
		if provider == "talos" {
			preflightStatus = "RUN (basic prerequisites only; comprehensive checks skipped)"
		}
	}
	add("Preflight checks", "Run the checks listed above; no mutation", preflightStatus)
	if provider == "flatcar" {
		if options.SkipKubeadm {
			add("kubeadm init/join + kubeconfig", "Use the existing control plane and supplied kubeconfig", "SKIP (--skip-kubeadm)")
		} else {
			add("kubeadm init", "Render and run init on "+nodes[0].Name, "RUN")
			add("Fetch kubeconfig", "Fetch admin.conf, save configured state, patch local bootstrap endpoint, validate", "RUN")
			add("Join remaining control planes", fmt.Sprintf("Join %d node(s) in configured order", len(nodes)-1), "RUN")
		}
		add("Install Cilium", "Helmfile sync selector name=cilium, then wait for the CNI", "RUN")
	} else {
		add("Apply Talos configuration", fmt.Sprintf("Render and apply machine configuration to %d node(s)", len(nodes)), "RUN")
		add("Bootstrap Talos", "Select a controller, bootstrap etcd, and wait for health", "RUN")
		add("Fetch kubeconfig", "Fetch and validate the admin kubeconfig", "RUN")
	}
	add("Wait for nodes", "Require the configured nodes to appear and become ready", "RUN")
	add("Create namespaces", "Apply all bootstrap namespaces", "RUN")
	conditionalPlanStep(&steps, "Apply initial resources", "Resolve listed resource secrets and server-side apply bootstrap/resources.yaml", options.SkipResources, "--skip-resources")
	conditionalPlanStep(&steps, "Apply CRDs", "Template CRD helmfile, apply Gateway API CRDs, wait for establishment", options.SkipCRDs, "--skip-crds")
	conditionalPlanStep(&steps, "Sync Helm releases", "Sync bootstrap/helmfile.d/01-apps.yaml", options.SkipHelmfile, "--skip-helmfile")
	conditionalPlanStep(&steps, "Wait for Flux", "Wait for controller, GitRepository, and Kustomization reconciliation", options.SkipHelmfile, "--skip-helmfile")
	for index := range steps {
		steps[index].Order = index + 1
	}
	return steps
}

func conditionalPlanStep(steps *[]bootstrapPlanStep, action, effect string, skipped bool, flag string) {
	status := "RUN"
	if skipped {
		status = "SKIP (" + flag + ")"
	}
	*steps = append(*steps, bootstrapPlanStep{Action: action, Effect: effect, Status: status})
}

func renderBootstrapPlan(plan bootstrapPlan, output string) (string, error) {
	if output == "json" {
		raw, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	if output != "" && output != "table" {
		return "", fmt.Errorf("unsupported output format %q (table, json)", output)
	}
	versionRows := make([][]string, 0, len(plan.Versions))
	for _, version := range plan.Versions {
		versionRows = append(versionRows, []string{version.Component, version.Version, version.Source})
	}
	nodeRows := make([][]string, 0, len(plan.Nodes))
	for _, node := range plan.Nodes {
		nodeRows = append(nodeRows, []string{fmt.Sprintf("%d", node.Order), node.Name, node.IP, node.Role})
	}
	checkRows := make([][]string, 0, len(plan.Preflight))
	for _, check := range plan.Preflight {
		checkRows = append(checkRows, []string{fmt.Sprintf("%d", check.Order), check.Status, check.Name, check.Effect})
	}
	secretRows := make([][]string, 0, len(plan.Secrets))
	for _, secret := range plan.Secrets {
		secretRows = append(secretRows, []string{secret.Key, secret.Backend, secret.Status})
	}
	artifactRows := make([][]string, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		artifactRows = append(artifactRows, []string{artifact.Action, artifact.Name, artifact.Effect})
	}
	vmRows := make([][]string, 0, len(plan.VMs))
	for _, vm := range plan.VMs {
		vmRows = append(vmRows, []string{vm.Node, vm.Hypervisor, fmt.Sprintf("%d", vm.VMID), vm.MAC,
			fmt.Sprintf("%d/%d", vm.Cores, vm.MemoryMB), fmt.Sprintf("%dGB@%s", vm.BootDiskGB, vm.BootStorage),
			fmt.Sprintf("%dGB@%s", vm.OpenEBSDiskGB, vm.OpenEBSStorage), vm.Ceph, vm.Network, vm.CPUAffinity, vm.NUMANode})
	}
	joinRows := renderBootstrapStepRows(plan.JoinSequence)
	stepRows := renderBootstrapStepRows(plan.Steps)
	return fmt.Sprintf("BOOTSTRAP PLAN (NO CHANGES WILL BE MADE)\nCluster: %s\nProvider: %s\nHypervisor: %s\nConfig: %s\nVM note: bootstrap does not create VMs; these are the exact effective parameters for the prerequisite deploy-vm operations.\n\nVersions\n%s\n\nNodes\n%s\n\nPreflight checks\n%s\n\nSecrets (key names and backend kinds only; values and references are never printed)\n%s\n\nTemplates and manifests\n%s\n\nVM creation parameters\n%s\n\nJoin sequence\n%s\n\nComplete ordered steps and exact effects\n%s",
		plan.Cluster, plan.Provider, plan.Hypervisor, plan.ConfigSource,
		ui.Table([]string{"COMPONENT", "VERSION", "SOURCE"}, versionRows),
		ui.Table([]string{"ORDER", "NODE", "IP", "IDENTITY"}, nodeRows),
		ui.Table([]string{"ORDER", "STATUS", "CHECK", "EXACT EFFECT"}, checkRows),
		ui.Table([]string{"KEY", "BACKEND", "STATUS"}, secretRows),
		ui.Table([]string{"ACTION", "NAME", "EXACT EFFECT"}, artifactRows),
		ui.Table([]string{"NODE", "HYPERVISOR", "VMID", "MAC", "CPU/MEM_MB", "BOOT", "OPENEBS", "CEPH", "NETWORK", "AFFINITY", "NUMA"}, vmRows),
		ui.Table([]string{"ORDER", "STATUS", "ACTION", "EXACT EFFECT"}, joinRows),
		ui.Table([]string{"ORDER", "STATUS", "ACTION", "EXACT EFFECT"}, stepRows)), nil
}

func renderBootstrapStepRows(steps []bootstrapPlanStep) [][]string {
	rows := make([][]string, 0, len(steps))
	for _, step := range steps {
		rows = append(rows, []string{fmt.Sprintf("%d", step.Order), step.Status, step.Action, step.Effect})
	}
	return rows
}
