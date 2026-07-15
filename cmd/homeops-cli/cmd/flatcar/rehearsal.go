package flatcar

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"homeops-cli/internal/common"
	versionconfig "homeops-cli/internal/config"
	flatcarinternal "homeops-cli/internal/flatcar"

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
	cfg := versionconfig.Get()
	vmProfile := options.Node.VM.ForProvider("flatcar")
	if provider == providerVSphere {
		vmProfile = options.Node.VM.ForProvider("vsphere")
	}
	deployOptions := deployVMOptions{
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
		powerOn:         true,
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
	ignition, err := renderIgnitionFn(env)
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
	nodes := []flatcarNode{{name: options.Node.Name, ignition: ignition}}
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
