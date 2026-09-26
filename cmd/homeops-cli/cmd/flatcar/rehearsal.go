package flatcar

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	fcosinternal "homeops-cli/internal/fcos"
	flatcarinternal "homeops-cli/internal/flatcar"
	"homeops-cli/internal/ssh"

	"github.com/spf13/cobra"
)

// RehearsalDeployOptions describes the one disposable node deployed by the
// cluster rehearsal. Identity and hardware come from cluster.test_node; the
// image source mirrors flatcar deploy-vm's existing provider-specific inputs.
type RehearsalDeployOptions struct {
	Node        versionconfig.Node
	Provider    string
	ImagePath   string
	ImageVolume string
	Join        flatcarinternal.KubeadmResult
	SSHUser     string
	Timeout     time.Duration
	// Boot, when set, creates the VM powered off and hands it to Boot before
	// its first start. Boot does any pre-boot disk work and must power the VM
	// on; the kubeadm wait and join follow. Proxmox only (replace-node swaps
	// the preserved scratch disk in here).
	Boot func(context.Context) error
}

// DeployRehearsalNode reuses the same render, staging, and hypervisor deployers
// as `flatcar deploy-vm`, then drives the existing kubeadm join orchestrator.
// Keeping this seam in cmd/flatcar avoids copying any provider deployment logic
// into the cross-cutting cluster rehearsal command.
func DeployRehearsalNode(ctx context.Context, options RehearsalDeployOptions) error {
	if options.Node.Name == "" || options.Node.IP == "" {
		return fmt.Errorf("rehearsal node name and ip are required")
	}
	if options.Timeout <= 0 {
		return fmt.Errorf("rehearsal deploy timeout must be greater than zero")
	}
	if err := flatcarinternal.ValidateJoinMaterial(options.Join.BootstrapToken, options.Join.CACertHash, options.Join.CertificateKey); err != nil {
		return fmt.Errorf("invalid rehearsal join material: %w", err)
	}

	provider, err := normalizeFlatcarProvider(options.Provider)
	if err != nil {
		return err
	}
	if options.Boot != nil && provider != providerProxmox {
		return fmt.Errorf("a powered-off deploy with a pre-boot step is only supported on Proxmox, not %s", provider)
	}
	cfg := versionconfig.Get()
	// The disposable node's OS comes from test_node.os (else cluster.os), so a
	// join drill exercises exactly the OS the next production rebuild will run.
	osFamily := cfg.OSForNode(options.Node)
	vmProfile := options.Node.VM.ForProvider("flatcar")
	if provider == providerVSphere {
		vmProfile = options.Node.VM.ForProvider("vsphere")
	}
	deployOptions := deployVMOptions{
		osFamily:        osFamily,
		provider:        provider,
		nodes:           []string{options.Node.Name},
		imagePath:       options.ImagePath,
		imageVolume:     options.ImageVolume,
		snippetsDir:     cfg.Hypervisors.Proxmox.SnippetsDir,
		pveSSHUser:      cfg.Hypervisors.Proxmox.SSHUser,
		pveSSHPort:      "22",
		vsphereTemplate: cfg.Hypervisors.VSphere.Template,
		datastore:       vmProfile.BootStorage,
		vsphereNetwork:  cfg.Hypervisors.VSphere.VM.NetworkBridge,
		vcpus:           cfg.Hypervisors.VSphere.VM.Cores,
		memory:          cfg.Hypervisors.VSphere.VM.MemoryMB,
		truenasPool:     cfg.TrueNASPool(),
		networkBridge:   cfg.Hypervisors.TrueNAS.VM.NetworkBridge,
		truenasSSHUser:  cfg.Hypervisors.TrueNAS.SSHUser,
		truenasPort:     443,
		vip:             cfg.Cluster.ControlPlaneVIP,
		nodeInterface:   cfg.Cluster.NodeInterface,
		concurrent:      1,
		powerOn:         options.Boot == nil,
		stream:          fcosinternal.DefaultStream,
	}
	if err := validateDeployVMOptions(provider, deployOptions); err != nil {
		return err
	}

	env, err := buildNodeEnv(options.Node.Name, deployOptions.vip, "", "", deployOptions.nodeInterface)
	if err != nil {
		return err
	}
	env.BootstrapToken = options.Join.BootstrapToken
	env.CACertHash = options.Join.CACertHash
	env.CertificateKey = options.Join.CertificateKey
	ignition, err := renderIgnitionForOS(deployOptions.family(), env)
	if err != nil {
		return fmt.Errorf("render rehearsal ignition: %w", err)
	}
	joinConfig, err := renderKubeadmJoinFn(env)
	if err != nil {
		return fmt.Errorf("render rehearsal kubeadm join config: %w", err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard)
	nodes := []flatcarNode{{name: options.Node.Name, ignition: ignition, osFamily: deployOptions.family()}}
	logger := common.NewColorLogger()
	switch provider {
	case providerVSphere:
		err = deployVSphere(cmd, deployOptions, logger, nodes)
	case providerTrueNAS:
		err = deployTrueNAS(cmd, deployOptions, logger, nodes)
	default:
		err = deployProxmox(cmd, deployOptions, logger, nodes)
	}
	if err != nil {
		return err
	}
	if options.Boot != nil {
		if err := options.Boot(ctx); err != nil {
			return fmt.Errorf("pre-boot step for %s: %w", options.Node.Name, err)
		}
	}

	orchestrator := flatcarinternal.NewOrchestrator(flatcarinternal.OrchestratorConfig{
		SSHUser: options.SSHUser,
		Port:    strconv.Itoa(cfg.Cluster.NodeSSHPort),
	})
	waitCtx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	if err := orchestrator.WaitForKubeadm(waitCtx, options.Node.IP); err != nil {
		return err
	}
	return orchestrator.JoinControlPlane(options.Node.IP, joinConfig)
}

// RunPVERootCommand runs one command on the Proxmox host over the configured
// SSH session (hypervisors.proxmox.ssh_user / ssh_key; sudo when the user is
// not root) and returns its output. It is the transport for the qm/pvesm
// steps that the Proxmox API does not expose (disk reassignment between VMs).
func RunPVERootCommand(command string) (string, error) {
	host, _, _, _, err := getProxmoxCredentialsFn()
	if err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("proxmox host is not configured")
	}
	user := versionconfig.Get().Hypervisors.Proxmox.SSHUser
	if user == "" {
		user = "root"
	}
	client := ssh.NewSSHClient(proxmoxSSHConfig(host, user, "22"))
	if err := client.Connect(); err != nil {
		return "", fmt.Errorf("connect to %s@%s: %w", user, host, err)
	}
	defer func() { _ = client.Close() }()
	if user != "root" {
		command = "sudo " + command
	}
	return client.ExecuteCommand(command)
}
