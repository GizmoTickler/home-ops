package workstation

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestEnv fakes the platform: GOOS, /etc/os-release, which binaries are
// on PATH, version output, and records brew invocations.
type setupTestEnv struct {
	onPath   map[string]bool
	brewRuns [][]string
	brewErr  error
	selected []string
}

func newSetupTestEnv(t *testing.T, goos string, distro string, binaries ...string) *setupTestEnv {
	t.Helper()
	env := &setupTestEnv{onPath: map[string]bool{}}
	for _, b := range binaries {
		env.onPath[b] = true
	}

	origGOOS, origLook, origOSRelease, origVersion, origBrew, origChoose := runtimeGOOS, lookPathFn, osReleaseReadFn, toolVersionFn, brewInstallFn, chooseMultiFn
	t.Cleanup(func() {
		runtimeGOOS, lookPathFn, osReleaseReadFn, toolVersionFn, brewInstallFn, chooseMultiFn = origGOOS, origLook, origOSRelease, origVersion, origBrew, origChoose
	})

	runtimeGOOS = goos
	lookPathFn = func(name string) (string, error) {
		if env.onPath[name] {
			return "/fake/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	osReleaseReadFn = func() (string, error) {
		if distro == "" {
			return "", errors.New("no os-release")
		}
		return fmt.Sprintf("ID=rocky\nPRETTY_NAME=%q\n", distro), nil
	}
	toolVersionFn = func(binary string, args ...string) (string, error) {
		return binary + " v9.9.9\nextra line", nil
	}
	brewInstallFn = func(args ...string) error {
		env.brewRuns = append(env.brewRuns, args)
		return env.brewErr
	}
	chooseMultiFn = func(prompt string, options []string, limit int) ([]string, error) {
		if env.selected != nil {
			return env.selected, nil
		}
		return options, nil
	}
	return env
}

func TestDetectPlatform(t *testing.T) {
	t.Run("linux distro with brew", func(t *testing.T) {
		newSetupTestEnv(t, "linux", "Rocky Linux 9.5 (Blue Onyx)", "brew")
		platform := detectPlatform()
		assert.Equal(t, "linux", platform.OS)
		assert.Equal(t, "Rocky Linux 9.5 (Blue Onyx)", platform.Distro)
		assert.True(t, platform.Brew)
		assert.Contains(t, platform.describe(), "Rocky Linux 9.5")
		assert.Contains(t, platform.describe(), "Homebrew available")
	})

	t.Run("macos without brew", func(t *testing.T) {
		newSetupTestEnv(t, "darwin", "")
		platform := detectPlatform()
		assert.False(t, platform.Brew)
		assert.Contains(t, platform.describe(), "macOS")
		assert.Contains(t, platform.describe(), "NOT found")
	})
}

func TestScanToolsMarksCasksUnavailableOnLinux(t *testing.T) {
	newSetupTestEnv(t, "linux", "Ubuntu 24.04", "brew", "kubectl")
	statuses := scanTools(detectPlatform())

	byName := map[string]toolStatus{}
	for _, status := range statuses {
		byName[status.Tool.Name] = status
	}
	assert.True(t, byName["kubectl"].Installed)
	assert.Contains(t, byName["kubectl"].Version, "v9.9.9")
	assert.False(t, byName["op"].Installed)
	assert.Contains(t, byName["op"].Skip, "macOS cask")
	assert.False(t, byName["helm"].Installed)
	assert.Empty(t, byName["helm"].Skip, "regular formulae stay installable on Linux")
}

func TestRunSetupAllInstallsMissing(t *testing.T) {
	env := newSetupTestEnv(t, "darwin", "", "brew", "kubectl", "helm", "jq")
	require.NoError(t, runSetup(true, false, false))

	var installed []string
	for _, run := range env.brewRuns {
		require.Equal(t, "install", run[0])
		installed = append(installed, run[len(run)-1])
	}
	assert.Contains(t, installed, "helmfile")
	assert.Contains(t, installed, "fluxcd/tap/flux")
	assert.NotContains(t, installed, "kubernetes-cli", "already-installed tools are not reinstalled")
	// the 1Password cask install must carry --cask on macOS
	foundCask := false
	for _, run := range env.brewRuns {
		if run[len(run)-1] == "1password-cli" {
			foundCask = true
			assert.Contains(t, run, "--cask")
		}
	}
	assert.True(t, foundCask)
}

func TestRunSetupInteractiveSelection(t *testing.T) {
	env := newSetupTestEnv(t, "darwin", "", "brew", "kubectl")
	env.selected = []string{"helm"}
	require.NoError(t, runSetup(false, false, false))
	require.Len(t, env.brewRuns, 1)
	assert.Equal(t, []string{"install", "helm"}, env.brewRuns[0])
}

func TestRunSetupUpgradeBringsInstalledToLatest(t *testing.T) {
	env := newSetupTestEnv(t, "darwin", "", "brew", "kubectl", "helm")
	env.selected = []string{} // install nothing new
	require.NoError(t, runSetup(false, true, false))
	require.NotEmpty(t, env.brewRuns)
	last := env.brewRuns[len(env.brewRuns)-1]
	assert.Equal(t, "upgrade", last[0])
	assert.Contains(t, last, "kubernetes-cli")
	assert.Contains(t, last, "helm")
}

func TestRunSetupDryRunInstallsNothing(t *testing.T) {
	env := newSetupTestEnv(t, "darwin", "", "brew")
	require.NoError(t, runSetup(true, true, true))
	assert.Empty(t, env.brewRuns)
}

func TestRunSetupRequiresBrew(t *testing.T) {
	env := newSetupTestEnv(t, "linux", "Debian GNU/Linux 13", "kubectl")
	err := runSetup(true, false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "brew.sh")
	assert.Empty(t, env.brewRuns)
}

func TestRunSetupReportsInstallFailures(t *testing.T) {
	env := newSetupTestEnv(t, "darwin", "", "brew", "kubectl")
	env.selected = []string{"helm", "jq"}
	env.brewErr = errors.New("formula exploded")
	err := runSetup(false, false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 tool(s) failed")
}

func TestFirstLineTruncates(t *testing.T) {
	assert.Equal(t, "short", firstLine("short\nrest"))
	long := strings.Repeat("x", 60)
	assert.Len(t, firstLine(long), 48)
	assert.Equal(t, "", firstLine("  \n"))
}
