package workstation

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"homeops-cli/internal/common"
	"homeops-cli/internal/ui"
)

// Seams for hermetic tests.
var (
	lookPathFn      = exec.LookPath
	osReleaseReadFn = func() (string, error) {
		raw, err := os.ReadFile("/etc/os-release")
		return string(raw), err
	}
	toolVersionFn = func(binary string, args ...string) (string, error) {
		out, err := common.Output(binary, args...)
		return string(out), err
	}
	brewInstallFn = func(args ...string) error {
		return runInteractiveFunc(nil, os.Stdout, os.Stderr, "brew", args...)
	}
	chooseMultiFn = ui.ChooseMulti
)

// workstationTool is one entry of the curated tool catalog.
type workstationTool struct {
	Name        string // display + selection key
	Binary      string // what to look for on PATH
	Description string
	Brew        string   // Homebrew formula (tap-qualified when needed)
	Cask        bool     // macOS cask (not installable via Homebrew on Linux)
	VersionArgs []string // how to ask the binary its version (default: --version)
}

// toolCatalog is the curated set of tools the repo's workflows use. Installs
// prefer Homebrew where available, but some tools remain platform-specific
// and are surfaced as unavailable rather than overpromised.
var toolCatalog = []workstationTool{
	{Name: "kubectl", Binary: "kubectl", Brew: "kubernetes-cli", Description: "Kubernetes CLI", VersionArgs: []string{"version", "--client"}},
	{Name: "helm", Binary: "helm", Brew: "helm", Description: "Kubernetes package manager", VersionArgs: []string{"version", "--short"}},
	{Name: "helmfile", Binary: "helmfile", Brew: "helmfile", Description: "declarative Helm releases (bootstrap)"},
	{Name: "flux", Binary: "flux", Brew: "fluxcd/tap/flux", Description: "Flux CD GitOps CLI", VersionArgs: []string{"--version"}},
	{Name: "kustomize", Binary: "kustomize", Brew: "kustomize", Description: "Kubernetes manifest overlays", VersionArgs: []string{"version"}},
	{Name: "kubeconform", Binary: "kubeconform", Brew: "kubeconform", Description: "manifest schema validation", VersionArgs: []string{"-v"}},
	{Name: "cilium", Binary: "cilium", Brew: "cilium-cli", Description: "Cilium CNI CLI", VersionArgs: []string{"version", "--client"}},
	{Name: "talosctl", Binary: "talosctl", Brew: "siderolabs/tap/talosctl", Description: "Talos Linux CLI (legacy provider)", VersionArgs: []string{"version", "--client", "--short"}},
	{Name: "k9s", Binary: "k9s", Brew: "k9s", Description: "terminal Kubernetes UI", VersionArgs: []string{"version", "--short"}},
	{Name: "stern", Binary: "stern", Brew: "stern", Description: "multi-pod log tailing"},
	{Name: "kubecolor", Binary: "kubecolor", Brew: "kubecolor", Description: "colorized kubectl output"},
	{Name: "krew", Binary: "kubectl-krew", Brew: "krew", Description: "kubectl plugin manager", VersionArgs: []string{"version"}},
	{Name: "task", Binary: "task", Brew: "go-task/tap/go-task", Description: "go-task runner"},
	{Name: "gh", Binary: "gh", Brew: "gh", Description: "GitHub CLI"},
	{Name: "jq", Binary: "jq", Brew: "jq", Description: "JSON processor"},
	{Name: "yq", Binary: "yq", Brew: "yq", Description: "YAML processor"},
	{Name: "minijinja", Binary: "minijinja-cli", Brew: "minijinja-cli", Description: "template rendering (talos configs)"},
	{Name: "cloudflared", Binary: "cloudflared", Brew: "cloudflared", Description: "Cloudflare tunnel client"},
	{Name: "golangci-lint", Binary: "golangci-lint", Brew: "golangci-lint", Description: "Go linter (CLI development)"},
	{Name: "op", Binary: "op", Brew: "1password-cli", Cask: true, Description: "1Password CLI (op:// secret backend)"},
}

// platformInfo describes the detected workstation.
type platformInfo struct {
	OS     string // darwin | linux
	Arch   string
	Distro string // pretty distro name on Linux ("" on macOS)
	Brew   bool   // Homebrew available
}

func (p platformInfo) describe() string {
	name := "macOS"
	if p.OS == "linux" {
		name = "Linux"
		if p.Distro != "" {
			name = p.Distro
		}
	} else if p.OS != "darwin" {
		name = p.OS
	}
	brew := "Homebrew available"
	if !p.Brew {
		brew = "Homebrew NOT found"
	}
	return fmt.Sprintf("%s (%s), %s", name, p.Arch, brew)
}

// detectPlatform inspects the OS, CPU architecture, Linux distro, and
// Homebrew availability.
func detectPlatform() platformInfo {
	info := platformInfo{OS: runtimeGOOS, Arch: runtimeGOARCH}
	if info.OS == "linux" {
		if raw, err := osReleaseReadFn(); err == nil {
			for _, line := range strings.Split(raw, "\n") {
				if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
					info.Distro = strings.Trim(strings.TrimSpace(value), `"`)
					break
				}
			}
		}
	}
	if _, err := lookPathFn("brew"); err == nil {
		info.Brew = true
	}
	return info
}

// toolStatus is the per-tool result of the workstation scan.
type toolStatus struct {
	Tool      workstationTool
	Installed bool
	Version   string
	Skip      string // non-empty: not installable on this platform (reason)
}

// firstLine returns the trimmed first non-empty line of command output,
// truncated to keep the table readable.
func firstLine(out string) string {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 48 {
			line = line[:45] + "..."
		}
		return line
	}
	return ""
}

// scanTools resolves install/version state for the catalog on this platform.
func scanTools(platform platformInfo) []toolStatus {
	statuses := make([]toolStatus, 0, len(toolCatalog))
	for _, tool := range toolCatalog {
		status := toolStatus{Tool: tool}
		if tool.Cask && platform.OS != "darwin" {
			status.Skip = "macOS cask; install via your distro's package repo"
		}
		if _, err := lookPathFn(tool.Binary); err == nil {
			status.Installed = true
			args := tool.VersionArgs
			if len(args) == 0 {
				args = []string{"--version"}
			}
			if out, err := toolVersionFn(tool.Binary, args...); err == nil {
				status.Version = firstLine(out)
			} else {
				status.Version = "installed"
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// brewInstructions is shown when Homebrew is missing.
func brewInstructions(platform platformInfo) string {
	target := "macOS"
	if platform.OS == "linux" {
		target = "Linux"
	}
	return fmt.Sprintf(`Homebrew is required: it is the one package manager carrying all of these
tools at their latest versions on both macOS and Linux.

Install it on %s with the official installer (https://brew.sh), then re-run:
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`, target)
}

// newSetupCommand creates the OS-aware tool installer.
func newSetupCommand() *cobra.Command {
	var all, upgrade, dryRun bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Detect the OS and install the repo's tooling at latest versions",
		Long: `Detect the operating system, scan the curated tool catalog (kubectl, helm,
helmfile, flux, talosctl, jq, ...), and install what's missing through
Homebrew — the one package manager that carries all of these at their latest
versions on both macOS and Linux. Interactive multi-select by default;
--all installs every missing tool unprompted, --upgrade also brings the
already-installed ones to their latest versions.`,
		Example: `  homeops-cli workstation setup             # scan + pick what to install
  homeops-cli workstation setup --all       # install everything missing
  homeops-cli workstation setup --all --upgrade   # ...and upgrade the rest
  homeops-cli workstation setup --dry-run   # just show the table`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetup(all, upgrade, dryRun)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "install every missing tool without prompting")
	cmd.Flags().BoolVar(&upgrade, "upgrade", false, "also upgrade already-installed tools to their latest versions")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the scan table without installing anything")
	return cmd
}

func runSetup(all, upgrade, dryRun bool) error {
	logger := common.NewColorLogger()
	platform := detectPlatform()
	logger.Info("Detected: %s", platform.describe())

	statuses := scanTools(platform)
	statusByName := make(map[string]toolStatus, len(statuses))
	rows := make([][]string, 0, len(statuses))
	var missing, installed []string
	for _, status := range statuses {
		statusByName[status.Tool.Name] = status
		state := "missing"
		switch {
		case status.Installed:
			state = "installed"
			installed = append(installed, status.Tool.Name)
		case status.Skip != "":
			state = "unavailable"
		default:
			missing = append(missing, status.Tool.Name)
		}
		detail := status.Version
		if !status.Installed && status.Skip != "" {
			detail = status.Skip
		}
		rows = append(rows, []string{status.Tool.Name, state, detail, status.Tool.Description})
	}
	ui.PrintTable([]string{"TOOL", "STATUS", "VERSION", "PURPOSE"}, rows)

	if dryRun {
		logger.Info("%d installed, %d missing (dry run — nothing installed)", len(installed), len(missing))
		return nil
	}
	if !platform.Brew && (len(missing) > 0 || upgrade) {
		return fmt.Errorf("%s", brewInstructions(platform))
	}

	// Decide what to install.
	var targets []string
	switch {
	case len(missing) == 0:
		logger.Success("All catalog tools are installed")
	case all:
		targets = missing
	default:
		selected, err := chooseMultiFn("Select tools to install (latest versions via Homebrew):", missing, 0)
		if err != nil {
			if ui.IsCancellation(err) {
				return nil
			}
			return fmt.Errorf("tool selection failed: %w (use --all in non-interactive sessions)", err)
		}
		targets = selected
	}

	failures := 0
	for _, name := range targets {
		status := statusByName[name]
		brewArgs := []string{"install"}
		if status.Tool.Cask {
			brewArgs = append(brewArgs, "--cask")
		}
		brewArgs = append(brewArgs, status.Tool.Brew)
		logger.Info("Installing %s (%s)...", name, status.Tool.Brew)
		if err := brewInstallFn(brewArgs...); err != nil {
			failures++
			logger.Error("install %s: %v", name, err)
		}
	}

	if upgrade && len(installed) > 0 {
		// brew upgrade only touches outdated formulae, so this is exactly
		// "bring everything to latest" without reinstall churn.
		var formulae []string
		for _, name := range installed {
			status := statusByName[name]
			if status.Tool.Brew == "" || status.Tool.Cask && platform.OS != "darwin" {
				continue
			}
			formulae = append(formulae, status.Tool.Brew)
		}
		sort.Strings(formulae)
		logger.Info("Upgrading %d installed tools to latest...", len(formulae))
		if err := brewInstallFn(append([]string{"upgrade"}, formulae...)...); err != nil {
			// brew upgrade exits non-zero in some already-up-to-date cask
			// configurations; report without failing the whole setup.
			logger.Warn("brew upgrade finished with: %v", err)
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d tool(s) failed to install", failures)
	}
	if len(targets) > 0 {
		logger.Success("Installed %d tool(s) at their latest versions", len(targets))
	}
	return nil
}
