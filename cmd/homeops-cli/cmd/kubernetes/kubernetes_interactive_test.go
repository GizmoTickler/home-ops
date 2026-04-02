package kubernetes

import (
	"fmt"
	"testing"
	"time"

	"homeops-cli/internal/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectKubectlResource(t *testing.T) {
	oldOutput := kubectlOutputFn
	oldChoose := chooseOptionFn
	t.Cleanup(func() {
		kubectlOutputFn = oldOutput
		chooseOptionFn = oldChoose
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		assert.Equal(t, []string{"get", "pvc", "-n", "default", "-o", "jsonpath={.items[*].metadata.name}"}, args)
		return []byte("data logs"), nil
	}
	chooseOptionFn = func(prompt string, options []string) (string, error) {
		assert.Equal(t, "Pick a PVC:", prompt)
		assert.Equal(t, []string{"data", "logs"}, options)
		return "logs", nil
	}

	selected, err := selectKubectlResource("default", "pvc", "Pick a PVC:")
	require.NoError(t, err)
	assert.Equal(t, "logs", selected)
}

func TestSelectKubectlResourceCancellation(t *testing.T) {
	oldOutput := kubectlOutputFn
	oldChoose := chooseOptionFn
	t.Cleanup(func() {
		kubectlOutputFn = oldOutput
		chooseOptionFn = oldChoose
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		return []byte("data"), nil
	}
	chooseOptionFn = func(prompt string, options []string) (string, error) {
		return "", fmt.Errorf("cancelled by user")
	}

	selected, err := selectKubectlResource("default", "pvc", "Pick a PVC:")
	require.NoError(t, err)
	assert.Empty(t, selected)
}

func TestEnsureKubectlPlugin(t *testing.T) {
	oldLookPath := lookPathFn
	oldInstall := installKubectlPluginFn
	t.Cleanup(func() {
		lookPathFn = oldLookPath
		installKubectlPluginFn = oldInstall
	})

	called := 0
	lookPathFn = func(file string) (string, error) {
		assert.Equal(t, "kubectl-browse-pvc", file)
		return "", fmt.Errorf("missing")
	}
	installKubectlPluginFn = func(plugin string) error {
		called++
		assert.Equal(t, "browse-pvc", plugin)
		return nil
	}

	require.NoError(t, ensureKubectlPlugin("kubectl-browse-pvc", "browse-pvc"))
	assert.Equal(t, 1, called)
}

func TestForceDeletePodsWithPrefixDeletesAllMatches(t *testing.T) {
	oldOutput := kubectlOutputFn
	oldRun := kubectlRunFn
	t.Cleanup(func() {
		kubectlOutputFn = oldOutput
		kubectlRunFn = oldRun
	})

	var deleted [][]string
	kubectlOutputFn = func(args ...string) ([]byte, error) {
		assert.Equal(t, []string{"get", "pods", "-n", "default", "-o", "jsonpath={.items[*].metadata.name}"}, args)
		return []byte("browse-a app browse-b"), nil
	}
	kubectlRunFn = func(args ...string) error {
		deleted = append(deleted, append([]string(nil), args...))
		return nil
	}

	err := forceDeletePodsWithPrefix("default", "browse-", common.NewColorLogger())
	require.NoError(t, err)
	require.Len(t, deleted, 2)
	assert.Equal(t, []string{"delete", "pod", "browse-a", "-n", "default", "--force", "--grace-period=0"}, deleted[0])
	assert.Equal(t, []string{"delete", "pod", "browse-b", "-n", "default", "--force", "--grace-period=0"}, deleted[1])
}

func TestForceDeletePodsWithPrefixReturnsAggregateError(t *testing.T) {
	oldOutput := kubectlOutputFn
	oldRun := kubectlRunFn
	t.Cleanup(func() {
		kubectlOutputFn = oldOutput
		kubectlRunFn = oldRun
	})

	kubectlOutputFn = func(args ...string) ([]byte, error) {
		return []byte("browse-a browse-b"), nil
	}
	kubectlRunFn = func(args ...string) error {
		if args[2] == "browse-b" {
			return fmt.Errorf("delete failed")
		}
		return nil
	}

	err := forceDeletePodsWithPrefix("default", "browse-", common.NewColorLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "browse-b")
}

func TestBrowsePVCCleansAllStalePods(t *testing.T) {
	oldLookPath := lookPathFn
	oldRun := kubectlRunFn
	oldOutput := kubectlOutputFn
	oldInteractive := kubectlRunInteractiveFn
	oldSleep := sleepFn
	t.Cleanup(func() {
		lookPathFn = oldLookPath
		kubectlRunFn = oldRun
		kubectlOutputFn = oldOutput
		kubectlRunInteractiveFn = oldInteractive
		sleepFn = oldSleep
	})

	var deleted []string
	lookPathFn = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	kubectlRunFn = func(args ...string) error {
		if len(args) >= 4 && args[0] == "--namespace" && args[2] == "get" && args[3] == "persistentvolumeclaims" {
			return nil
		}
		if len(args) >= 3 && args[0] == "delete" && args[1] == "pod" {
			deleted = append(deleted, args[2])
			return nil
		}
		return nil
	}
	kubectlOutputFn = func(args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "get" && args[1] == "pods" {
			return []byte("browse-a browse-b"), nil
		}
		return nil, fmt.Errorf("unexpected args: %v", args)
	}
	interactiveCalls := 0
	kubectlRunInteractiveFn = func(args ...string) error {
		interactiveCalls++
		assert.Equal(t, []string{"browse-pvc", "--namespace", "default", "--image", "alpine:latest", "data"}, args)
		return nil
	}
	sleepFn = func(time.Duration) {}

	err := browsePVC("default", "data", "alpine:latest")
	require.NoError(t, err)
	assert.Equal(t, 1, interactiveCalls)
	assert.Equal(t, []string{"browse-a", "browse-b"}, deleted)
}

func TestNodeShellInstallsPluginAndRunsInteractive(t *testing.T) {
	oldLookPath := lookPathFn
	oldInstall := installKubectlPluginFn
	oldRun := kubectlRunFn
	oldInteractive := kubectlRunInteractiveFn
	t.Cleanup(func() {
		lookPathFn = oldLookPath
		installKubectlPluginFn = oldInstall
		kubectlRunFn = oldRun
		kubectlRunInteractiveFn = oldInteractive
	})

	installs := 0
	lookPathFn = func(file string) (string, error) {
		return "", fmt.Errorf("missing")
	}
	installKubectlPluginFn = func(plugin string) error {
		installs++
		assert.Equal(t, "node-shell", plugin)
		return nil
	}
	kubectlRunFn = func(args ...string) error {
		assert.Equal(t, []string{"get", "nodes", "worker-1"}, args)
		return nil
	}
	interactiveCalls := 0
	kubectlRunInteractiveFn = func(args ...string) error {
		interactiveCalls++
		assert.Equal(t, []string{"node-shell", "-n", "kube-system", "-x", "worker-1"}, args)
		return nil
	}

	err := nodeShell("worker-1")
	require.NoError(t, err)
	assert.Equal(t, 1, installs)
	assert.Equal(t, 1, interactiveCalls)
}
