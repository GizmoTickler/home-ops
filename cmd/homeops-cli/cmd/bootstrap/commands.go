package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"homeops-cli/internal/common"
)

func parseReplicaCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("replica count is empty")
	}
	var count int
	if _, err := fmt.Sscanf(value, "%d", &count); err != nil {
		return 0, fmt.Errorf("invalid replica count %q: %w", value, err)
	}
	return count, nil
}

func deploymentReadyFromState(state string) bool {
	parts := strings.Split(strings.TrimSpace(state), "/")
	if len(parts) != 2 {
		return false
	}

	readyReplicas, err := parseReplicaCount(parts[0])
	if err != nil {
		return false
	}
	totalReplicas, err := parseReplicaCount(parts[1])
	if err != nil {
		return false
	}

	return totalReplicas > 0 && readyReplicas == totalReplicas
}

func deploymentAndEndpointsReadyFromState(state, endpoints string) bool {
	parts := strings.Split(strings.TrimSpace(state), ":")
	if len(parts) < 2 {
		return false
	}
	if strings.TrimSpace(parts[1]) != "True" {
		return false
	}
	if !deploymentReadyFromState(parts[0]) {
		return false
	}
	return strings.TrimSpace(endpoints) != ""
}

// buildTalosctlCmd builds a talosctl command with optional talosconfig.
func buildTalosctlCmd(talosConfig string, args ...string) *exec.Cmd {
	if talosConfig != "" {
		cmdArgs := append([]string{"--talosconfig", talosConfig}, args...)
		return common.Command("talosctl", cmdArgs...)
	}
	return common.Command("talosctl", args...)
}

func buildTalosctlCmdContext(ctx context.Context, talosConfig string, args ...string) *exec.Cmd {
	if talosConfig != "" {
		cmdArgs := append([]string{"--talosconfig", talosConfig}, args...)
		return exec.CommandContext(ctx, "talosctl", cmdArgs...)
	}
	return exec.CommandContext(ctx, "talosctl", args...)
}

func buildKubectlCmd(config *BootstrapConfig, args ...string) *exec.Cmd {
	cmdArgs := append(append([]string{}, args...), "--kubeconfig", config.KubeConfig)
	return common.Command("kubectl", cmdArgs...)
}

func buildKubectlCmdContext(ctx context.Context, config *BootstrapConfig, args ...string) *exec.Cmd {
	cmdArgs := append(append([]string{}, args...), "--kubeconfig", config.KubeConfig)
	return exec.CommandContext(ctx, "kubectl", cmdArgs...)
}

func kubectlOutput(config *BootstrapConfig, args ...string) ([]byte, error) {
	return buildKubectlCmd(config, args...).Output()
}

func kubectlRun(config *BootstrapConfig, args ...string) error {
	return buildKubectlCmd(config, args...).Run()
}

func kubectlCombinedOutput(config *BootstrapConfig, args ...string) ([]byte, error) {
	return buildKubectlCmd(config, args...).CombinedOutput()
}

func kubectlCombinedOutputWithInput(config *BootstrapConfig, input io.Reader, args ...string) ([]byte, error) {
	cmd := buildKubectlCmd(config, args...)
	cmd.Stdin = input
	return cmd.CombinedOutput()
}

// runKubectlContext executes kubectl with a context-bound timeout/cancellation
// signal. Output is redacted before being returned via the error path.
func runKubectlContext(ctx context.Context, config *BootstrapConfig, args ...string) (common.CommandResult, error) {
	cmdArgs := append(append([]string{}, args...), "--kubeconfig", config.KubeConfig)
	return common.RunCommand(ctx, common.CommandOptions{
		Name: "kubectl",
		Args: cmdArgs,
	})
}

// runTalosctlContext executes talosctl with a context-bound timeout/cancellation
// signal. Output is redacted before being returned via the error path.
func runTalosctlContext(ctx context.Context, talosConfig string, args ...string) (common.CommandResult, error) {
	cmdArgs := args
	if talosConfig != "" {
		cmdArgs = append([]string{"--talosconfig", talosConfig}, args...)
	}
	return common.RunCommand(ctx, common.CommandOptions{
		Name: "talosctl",
		Args: cmdArgs,
	})
}

func redactCommandOutput(output []byte) string {
	return common.RedactCommandOutput(string(output))
}

func buildHelmfileCmd(tempDir string, config *BootstrapConfig, args ...string) *exec.Cmd {
	cmd := common.Command("helmfile", args...)
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("ROOT_DIR=%s", config.RootDir))
	return cmd
}
