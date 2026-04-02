package testutil

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHelpersAndEnvUtilities(t *testing.T) {
	dir, cleanup := TempDir(t)
	defer cleanup()

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	file := TempFile(t, dir, "demo-*.txt", []byte("hello"))
	AssertFileExists(t, file)
	AssertFileContains(t, file, "hello")

	restoreEnv := SetEnv(t, "HOMEOPS_TEST_ONE", "value")
	assert.Equal(t, "value", os.Getenv("HOMEOPS_TEST_ONE"))
	restoreEnv()
	assert.Empty(t, os.Getenv("HOMEOPS_TEST_ONE"))

	restoreEnvs := SetEnvs(t, map[string]string{
		"HOMEOPS_TEST_TWO":   "two",
		"HOMEOPS_TEST_THREE": "three",
	})
	assert.Equal(t, "two", os.Getenv("HOMEOPS_TEST_TWO"))
	assert.Equal(t, "three", os.Getenv("HOMEOPS_TEST_THREE"))
	restoreEnvs()
	assert.Empty(t, os.Getenv("HOMEOPS_TEST_TWO"))
	assert.Empty(t, os.Getenv("HOMEOPS_TEST_THREE"))

	configPath := CreateTestConfig(t, dir, map[string]string{"name": "demo"})
	assert.Equal(t, filepath.Join(dir, "config.yaml"), configPath)
	AssertFileContains(t, configPath, "test: config")

	CompareYAML(t, "a: 1\n", "a: 1\n")
}

func TestExecuteCommandAndCaptureOutput(t *testing.T) {
	root := &cobra.Command{
		Use: "demo",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "command-output")
			return nil
		},
	}

	output, err := ExecuteCommand(root)
	require.NoError(t, err)
	assert.Contains(t, output, "command-output")

	stdout, stderr, err := CaptureOutput(func() {
		fmt.Print("stdout-line")
		fmt.Fprint(os.Stderr, "stderr-line")
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "stdout-line")
	assert.Contains(t, stderr, "stderr-line")
}

func TestMocks(t *testing.T) {
	httpClient := NewMockHTTPClient()
	httpClient.AddResponse("https://example.com", 200, "ok")
	httpClient.AddError("https://error.example.com", errors.New("boom"))

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)

	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"https://example.com"}, httpClient.Calls)

	reqErr, err := http.NewRequest(http.MethodGet, "https://error.example.com", nil)
	require.NoError(t, err)
	_, err = httpClient.Do(reqErr)
	require.Error(t, err)

	reqMissing, err := http.NewRequest(http.MethodGet, "https://missing.example.com", nil)
	require.NoError(t, err)
	resp, err = httpClient.Do(reqMissing)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	pod := CreateTestPod("pod", "default")
	ns := CreateTestNamespace("default")
	pvc := CreateTestPVC("data", "default")
	kubeClient := MockKubernetesClient(pod, ns, pvc)

	podList, err := kubeClient.CoreV1().Pods("default").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, podList.Items, 1)

	talosClient := NewMockTalosClient()
	require.NoError(t, talosClient.ApplyConfiguration(t.Context(), "192.168.122.10", []byte("config")))
	assert.Equal(t, "config", talosClient.NodeConfigs["192.168.122.10"])

	trueNASClient := NewMockTrueNASClient()
	require.NoError(t, trueNASClient.CreateVM("vm1", 2, 4096, "iso"))
	require.NoError(t, trueNASClient.StartVM("vm1"))
	assert.Equal(t, "running", trueNASClient.VMs["vm1"].State)
	require.NoError(t, trueNASClient.StopVM("vm1"))
	require.NoError(t, trueNASClient.UploadISO("/tmp/iso", "vm.iso"))
	assert.Contains(t, trueNASClient.UploadedISOs, "vm.iso")

	onePasswordClient := NewMock1PasswordClient()
	secret, err := onePasswordClient.GetSecret("op://vault/item/field")
	require.NoError(t, err)
	assert.Equal(t, "secret-value", secret)

	executor := NewMockCommandExecutor()
	executor.AddOutput("kubectl get pods", "pod-1")
	output, err := executor.Execute("kubectl", "get", "pods")
	require.NoError(t, err)
	assert.Equal(t, "pod-1", output)
	assert.Contains(t, executor.Commands, "kubectl get pods")
}
