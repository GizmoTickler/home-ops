package workstation

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/common"
	"homeops-cli/internal/testutil"
)

func resetWorkstationTestSeams(t *testing.T) {
	t.Helper()

	originalCheckCLI := checkCLIFunc
	originalGetBrewfile := getBrewfileFunc
	originalCombinedOutput := combinedOutputFunc
	originalRunInteractive := runInteractiveFunc
	originalSpinWithFunc := spinWithFunc
	originalIsKrewInstalled := isKrewInstalledFunc
	originalInstallKrew := installKrewFunc
	originalRunKrewCommand := runKrewCommandFunc
	originalListKrewPlugins := listKrewPluginsFunc

	t.Cleanup(func() {
		checkCLIFunc = originalCheckCLI
		getBrewfileFunc = originalGetBrewfile
		combinedOutputFunc = originalCombinedOutput
		runInteractiveFunc = originalRunInteractive
		spinWithFunc = originalSpinWithFunc
		isKrewInstalledFunc = originalIsKrewInstalled
		installKrewFunc = originalInstallKrew
		runKrewCommandFunc = originalRunKrewCommand
		listKrewPluginsFunc = originalListKrewPlugins
	})
}

func TestNewCommand(t *testing.T) {
	cmd := NewCommand()

	// Test command structure
	assert.Equal(t, "workstation", cmd.Use)
	assert.Equal(t, "Setup workstation tools and dependencies", cmd.Short)
	assert.Contains(t, cmd.Long, "Commands for setting up workstation tools")

	// Test subcommands are present
	subcommands := cmd.Commands()
	assert.Len(t, subcommands, 2)

	var brewCmd, krewCmd bool
	for _, subcmd := range subcommands {
		switch subcmd.Use {
		case "brew":
			brewCmd = true
		case "krew":
			krewCmd = true
		}
	}
	assert.True(t, brewCmd, "brew subcommand should be present")
	assert.True(t, krewCmd, "krew subcommand should be present")
}

func TestNewBrewCommand(t *testing.T) {
	cmd := newBrewCommand()

	assert.Equal(t, "brew", cmd.Use)
	assert.Equal(t, "Install Homebrew packages from Brewfile", cmd.Short)
	assert.Contains(t, cmd.Long, "Install all packages defined in the Brewfile")
	assert.NotNil(t, cmd.RunE)
}

func TestNewKrewCommand(t *testing.T) {
	cmd := newKrewCommand()

	assert.Equal(t, "krew", cmd.Use)
	assert.Equal(t, "Install kubectl plugins using Krew", cmd.Short)
	assert.Contains(t, cmd.Long, "Install required kubectl plugins")
	assert.NotNil(t, cmd.RunE)
}

func TestWorkstationHelpOutput(t *testing.T) {
	cmd := NewCommand()

	output, err := testutil.ExecuteCommand(cmd, "--help")
	require.NoError(t, err)

	// Verify help output contains expected content
	assert.Contains(t, output, "Commands for setting up workstation tools")
	assert.Contains(t, output, "Available Commands:")
	assert.Contains(t, output, "brew")
	assert.Contains(t, output, "krew")
}

func TestBrewSubcommandHelp(t *testing.T) {
	cmd := NewCommand()

	output, err := testutil.ExecuteCommand(cmd, "brew", "--help")
	require.NoError(t, err)

	assert.Contains(t, output, "Install all packages defined in the Brewfile")
}

func TestKrewSubcommandHelp(t *testing.T) {
	cmd := NewCommand()

	output, err := testutil.ExecuteCommand(cmd, "krew", "--help")
	require.NoError(t, err)

	assert.Contains(t, output, "Install required kubectl plugins")
}

func TestIsKrewInstalled(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		restore := common.SetCommandFactoryForTesting(func(name string, args ...string) *exec.Cmd {
			return exec.Command("bash", "-c", "exit 0")
		})
		defer restore()

		assert.True(t, isKrewInstalled())
	})

	t.Run("not installed", func(t *testing.T) {
		restore := common.SetCommandFactoryForTesting(func(name string, args ...string) *exec.Cmd {
			return exec.Command("bash", "-c", "exit 1")
		})
		defer restore()

		assert.False(t, isKrewInstalled())
	})
}

func TestInstallBrewPackagesValidation(t *testing.T) {
	// Test that function validates brew is installed before proceeding
	// This will fail if brew is not installed, which is expected behavior

	// Save original PATH
	originalPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("PATH", originalPath)
	}()

	// Set empty PATH to simulate brew not being available
	_ = os.Setenv("PATH", "")

	err := installBrewPackages()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "homebrew is not installed")
}

func TestInstallBrewPackagesAlreadySatisfied(t *testing.T) {
	resetWorkstationTestSeams(t)

	var checkCalls int
	var installCalled bool

	checkCLIFunc = func(tools ...string) error {
		assert.Equal(t, []string{"brew"}, tools)
		return nil
	}
	getBrewfileFunc = func() (string, error) {
		return "brew \"jq\"\n", nil
	}
	combinedOutputFunc = func(name string, args ...string) ([]byte, error) {
		checkCalls++
		assert.Equal(t, "brew", name)
		assert.Equal(t, []string{"bundle", "check", "--file", args[3]}, args)
		return []byte("The Brewfile's dependencies are satisfied."), nil
	}
	spinWithFunc = func(_ string, fn func() error) error {
		return fn()
	}
	runInteractiveFunc = func(_ io.Reader, _, _ io.Writer, _ string, _ ...string) error {
		installCalled = true
		return nil
	}

	require.NoError(t, installBrewPackages())
	assert.Equal(t, 1, checkCalls)
	assert.False(t, installCalled)
}

func TestInstallBrewPackagesInstallsWhenCheckFails(t *testing.T) {
	resetWorkstationTestSeams(t)

	var installArgs []string
	checkCLIFunc = func(tools ...string) error {
		assert.Equal(t, []string{"brew"}, tools)
		return nil
	}
	getBrewfileFunc = func() (string, error) {
		return "brew \"jq\"\n", nil
	}
	combinedOutputFunc = func(name string, args ...string) ([]byte, error) {
		assert.Equal(t, "brew", name)
		assert.Len(t, args, 4)
		return []byte("Missing packages"), fmt.Errorf("bundle check failed")
	}
	spinWithFunc = func(_ string, fn func() error) error {
		return fn()
	}
	runInteractiveFunc = func(_ io.Reader, _, _ io.Writer, name string, args ...string) error {
		installArgs = append([]string{name}, args...)
		return nil
	}

	require.NoError(t, installBrewPackages())
	require.NotEmpty(t, installArgs)
	assert.Equal(t, "brew", installArgs[0])
	assert.Equal(t, []string{"bundle", "install", "--file"}, installArgs[1:4])
}

func TestInstallKrewPluginsValidation(t *testing.T) {
	// Test that function validates kubectl is installed before proceeding

	// Save original PATH
	originalPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("PATH", originalPath)
	}()

	// Set empty PATH to simulate kubectl not being available
	_ = os.Setenv("PATH", "")

	err := installKrewPlugins()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubectl is not installed")
}

func TestInstallKrewPluginsSkipsInstalledPlugins(t *testing.T) {
	resetWorkstationTestSeams(t)

	var commands [][]string

	checkCLIFunc = func(tools ...string) error {
		assert.Equal(t, []string{"kubectl"}, tools)
		return nil
	}
	spinWithFunc = func(_ string, fn func() error) error {
		return fn()
	}
	isKrewInstalledFunc = func() bool { return true }
	listKrewPluginsFunc = func() ([]string, error) {
		return []string{"ctx", "ns", "stern", "tail", "who-can"}, nil
	}
	runKrewCommandFunc = func(args ...string) error {
		commands = append(commands, append([]string{}, args...))
		return nil
	}

	require.NoError(t, installKrewPlugins())
	assert.Equal(t, [][]string{{"update"}}, commands)
}

func TestInstallKrewPluginsInstallsMissingPlugins(t *testing.T) {
	resetWorkstationTestSeams(t)

	var commands [][]string
	var installKrewCalled bool

	checkCLIFunc = func(tools ...string) error {
		assert.Equal(t, []string{"kubectl"}, tools)
		return nil
	}
	spinWithFunc = func(_ string, fn func() error) error {
		return fn()
	}
	isKrewInstalledFunc = func() bool { return false }
	installKrewFunc = func() error {
		installKrewCalled = true
		return nil
	}
	listKrewPluginsFunc = func() ([]string, error) {
		return []string{"ctx", "tail"}, nil
	}
	runKrewCommandFunc = func(args ...string) error {
		commands = append(commands, append([]string{}, args...))
		return nil
	}

	require.NoError(t, installKrewPlugins())
	assert.True(t, installKrewCalled)
	assert.Equal(t,
		[][]string{
			{"update"},
			{"install", "ns"},
			{"install", "stern"},
			{"install", "who-can"},
		},
		commands,
	)
}

// Integration tests for actual command execution
func TestWorkstationCommandIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	cmd := NewCommand()

	t.Run("workstation help", func(t *testing.T) {
		output, err := testutil.ExecuteCommand(cmd, "--help")
		require.NoError(t, err)
		assert.Contains(t, output, "workstation")
	})

	t.Run("brew help", func(t *testing.T) {
		output, err := testutil.ExecuteCommand(cmd, "brew", "--help")
		require.NoError(t, err)
		assert.Contains(t, output, "Homebrew")
	})

	t.Run("krew help", func(t *testing.T) {
		output, err := testutil.ExecuteCommand(cmd, "krew", "--help")
		require.NoError(t, err)
		assert.Contains(t, output, "kubectl plugins")
	})
}

func TestWorkstationErrorHandling(t *testing.T) {
	cmd := NewCommand()

	t.Run("invalid subcommand", func(t *testing.T) {
		_, err := testutil.ExecuteCommand(cmd, "invalid")
		assert.Error(t, err)
	})

	t.Run("brew with invalid flag", func(t *testing.T) {
		_, err := testutil.ExecuteCommand(cmd, "brew", "--invalid-flag")
		assert.Error(t, err)
	})

	t.Run("krew with invalid flag", func(t *testing.T) {
		_, err := testutil.ExecuteCommand(cmd, "krew", "--invalid-flag")
		assert.Error(t, err)
	})
}

// Benchmark tests for performance-sensitive operations
func BenchmarkNewCommand(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewCommand()
	}
}

func BenchmarkIsKrewInstalled(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = isKrewInstalled()
	}
}

// Table-driven tests for multiple scenarios
func TestRunKrewCommandArguments(t *testing.T) {
	restore := common.SetCommandFactoryForTesting(func(name string, args ...string) *exec.Cmd {
		full := append([]string{name}, args...)
		script := "printf '%s' \"$*\""
		cmdArgs := append([]string{"-c", script, "--"}, full...)
		return exec.Command("bash", cmdArgs...)
	})
	defer restore()

	err := runKrewCommand("install", "ctx")
	assert.NoError(t, err)
}

func TestNormalizeKrewOS(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr string
	}{
		{input: "darwin", want: "darwin"},
		{input: "linux", want: "linux"},
		{input: "windows", want: "windows"},
		{input: "solaris", wantErr: "unsupported operating system"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := normalizeKrewOS(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeKrewArch(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr string
	}{
		{input: "amd64", want: "amd64"},
		{input: "arm64", want: "arm64"},
		{input: "386", want: "x86"},
		{input: "ppc64", wantErr: "unsupported architecture"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := normalizeKrewArch(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestListInstalledKrewPlugins(t *testing.T) {
	restore := common.SetCommandFactoryForTesting(func(name string, args ...string) *exec.Cmd {
		return exec.Command("bash", "-c", "printf 'PLUGIN\\nctx\\nns\\n'")
	})
	defer restore()

	plugins, err := listInstalledKrewPlugins()
	require.NoError(t, err)
	assert.Equal(t, []string{"ctx", "ns"}, plugins)
}

func TestKrewDownloadInfo(t *testing.T) {
	oldOS := runtimeGOOS
	oldArch := runtimeGOARCH
	oldBaseURL := krewDownloadBaseURL
	t.Cleanup(func() {
		runtimeGOOS = oldOS
		runtimeGOARCH = oldArch
		krewDownloadBaseURL = oldBaseURL
	})

	runtimeGOOS = "linux"
	runtimeGOARCH = "amd64"
	krewDownloadBaseURL = "https://example.test/krew"

	name, url, err := krewDownloadInfo()
	require.NoError(t, err)
	assert.Equal(t, "krew-linux_amd64", name)
	assert.Equal(t, "https://example.test/krew/krew-linux_amd64.tar.gz", url)
}

func TestInstallKrew(t *testing.T) {
	executableName := "krew-linux_amd64"
	archiveBytes := buildKrewArchive(t, executableName)

	oldOS := runtimeGOOS
	oldArch := runtimeGOARCH
	oldBaseURL := krewDownloadBaseURL
	oldHTTPGet := httpGetFunc
	t.Cleanup(func() {
		runtimeGOOS = oldOS
		runtimeGOARCH = oldArch
		krewDownloadBaseURL = oldBaseURL
		httpGetFunc = oldHTTPGet
	})

	runtimeGOOS = "linux"
	runtimeGOARCH = "amd64"
	krewDownloadBaseURL = "https://example.test/krew"
	httpGetFunc = func(url string) (*http.Response, error) {
		require.True(t, strings.HasSuffix(url, executableName+".tar.gz"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(bytes.NewReader(archiveBytes)),
		}, nil
	}

	require.NoError(t, installKrew())
}

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "../escape",
		Mode: 0o644,
		Size: int64(len("bad")),
	}))
	_, err := tw.Write([]byte("bad"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	require.NoError(t, os.WriteFile(archivePath, buf.Bytes(), 0o644))

	err = extractTarGz(archivePath, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes destination")
}

func buildKrewArchive(t *testing.T, executableName string) []byte {
	t.Helper()

	script := "#!/bin/sh\nexit 0\n"
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: executableName,
		Mode: 0o755,
		Size: int64(len(script)),
	}))
	_, err := io.WriteString(tw, script)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}
