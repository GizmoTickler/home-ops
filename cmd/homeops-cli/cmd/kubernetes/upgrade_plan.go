package kubernetes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	shareddiff "homeops-cli/internal/diff"
)

type upgradePlanCandidate struct {
	Path    string
	Name    string
	Version string
	Line    int
}

type upgradePlanSetOptions struct {
	Version        string
	RepoRoot       string
	PlanFile       string
	Write          bool
	Commit         bool
	AllowDowngrade bool
}

var (
	strictKubernetesVersionRE = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	planVersionLineRE         = regexp.MustCompile(`^([ \t]*version:[ \t]*)(["']?)(v\d+\.\d+\.\d+)(["']?)([ \t]*(?:#.*)?)$`)
	upgradePlanGitRootFn      = func(ctx context.Context) (string, error) {
		out, err := runKubernetesCommandOutputCtx(ctx, "git", "rev-parse", "--show-toplevel")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	upgradePlanGitRunFn = func(ctx context.Context, args ...string) error {
		return runKubernetesCommandRunCtx(ctx, "git", args...)
	}
	upgradePlanLiveContextFn = buildUpgradeStatusReport
)

func newUpgradePlanCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade-plan",
		Short: "Safely update Git-owned System Upgrade Controller plans",
		Long:  "Edits only the Git checkout; this command never uses kubectl edit or mutates the live Plan.",
	}
	cmd.AddCommand(newUpgradePlanSetCommand())
	return cmd
}

func newUpgradePlanSetCommand() *cobra.Command {
	var opts upgradePlanSetOptions
	cmd := &cobra.Command{
		Use:          "set <version>",
		Short:        "Preview or write a surgical kubeadm Plan version change",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  homeops-cli k8s upgrade-plan set v1.36.2
  homeops-cli k8s upgrade-plan set v1.36.2 --write
  homeops-cli k8s upgrade-plan set v1.36.2 --write --commit
  homeops-cli k8s upgrade-plan set v1.36.2 --repo-root /path/to/home-ops --plan-file kubernetes/path/plan.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Version = args[0]
			return runUpgradePlanSet(cmd.Context(), opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.RepoRoot, "repo-root", "", "git repository root (default: git rev-parse --show-toplevel)")
	cmd.Flags().StringVar(&opts.PlanFile, "plan-file", "", "kubeadm Plan YAML path, relative to the repository root")
	cmd.Flags().BoolVar(&opts.Write, "write", false, "write the previewed scalar edit to the Plan file")
	cmd.Flags().BoolVar(&opts.Commit, "commit", false, "commit only the Plan file after --write (never pushes)")
	cmd.Flags().BoolVar(&opts.AllowDowngrade, "allow-downgrade", false, "allow a target below the current Plan version")
	return cmd
}

func runUpgradePlanSet(ctx context.Context, opts upgradePlanSetOptions, out io.Writer) error {
	if opts.Commit && !opts.Write {
		return fmt.Errorf("--commit requires --write")
	}
	repoRoot, err := resolveUpgradePlanRepoRoot(ctx, opts.RepoRoot)
	if err != nil {
		return err
	}
	candidate, err := discoverKubeadmPlan(repoRoot, opts.PlanFile)
	if err != nil {
		return err
	}
	warning, err := validateUpgradePlanVersion(opts.Version, candidate.Version, opts.AllowDowngrade)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(candidate.Path) // #nosec G304 -- selected local Git file, constrained to repo root
	if err != nil {
		return fmt.Errorf("read kubeadm Plan %s: %w", candidate.Path, err)
	}
	edited, err := editUpgradePlanVersion(original, candidate, opts.Version)
	if err != nil {
		return err
	}
	relPath, err := filepath.Rel(repoRoot, candidate.Path)
	if err != nil {
		return fmt.Errorf("make Plan path relative to repository: %w", err)
	}

	_, _ = fmt.Fprintln(out, renderUpgradePlanLiveContext(ctx))
	_, _ = fmt.Fprintf(out, "Plan file: %s\n", candidate.Path)
	if warning != "" {
		_, _ = fmt.Fprintf(out, "WARN: %s\n", warning)
	}
	if bytes.Equal(original, edited) {
		_, _ = fmt.Fprintf(out, "No change: kubeadm Plan is already %s\n", opts.Version)
		return nil
	}
	_, _ = fmt.Fprintln(out, strings.TrimSuffix(shareddiff.UnifiedContext(filepath.ToSlash(relPath), original, edited, 3), "\n"))
	if !opts.Write {
		_, _ = fmt.Fprintln(out, "Dry run: pass --write to apply this Git-only change.")
		return nil
	}
	info, err := os.Stat(candidate.Path)
	if err != nil {
		return fmt.Errorf("stat kubeadm Plan %s: %w", candidate.Path, err)
	}
	if err := os.WriteFile(candidate.Path, edited, info.Mode().Perm()); err != nil { // #nosec G703 -- discovery constrains candidates to the selected repository root
		return fmt.Errorf("write kubeadm Plan %s: %w", candidate.Path, err)
	}
	_, _ = fmt.Fprintln(out, "Wrote Git-owned Plan file; the live Plan was not edited.")
	if opts.Commit {
		message := "feat(system-upgrade): bump kubeadm plan to " + opts.Version
		if err := upgradePlanGitRunFn(ctx, "-C", repoRoot, "commit", "--only", "-m", message, "--", relPath); err != nil {
			return fmt.Errorf("commit kubeadm Plan change: %w", err)
		}
		_, _ = fmt.Fprintf(out, "Committed: %s\n", message)
	}
	return nil
}

func resolveUpgradePlanRepoRoot(ctx context.Context, explicit string) (string, error) {
	root := strings.TrimSpace(explicit)
	if root == "" {
		var err error
		root, err = upgradePlanGitRootFn(ctx)
		if err != nil {
			return "", fmt.Errorf("find git repository root: %w", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root %s: %w", root, err)
	}
	if info, err := os.Stat(filepath.Join(abs, "kubernetes")); err != nil || !info.IsDir() {
		return "", fmt.Errorf("repository root %s has no kubernetes directory", abs)
	}
	return filepath.Clean(abs), nil
}

func discoverKubeadmPlan(repoRoot, planFile string) (upgradePlanCandidate, error) {
	if strings.TrimSpace(planFile) != "" {
		path := planFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoRoot, path)
		}
		path = filepath.Clean(path)
		if err := ensurePathWithinRepo(repoRoot, path); err != nil {
			return upgradePlanCandidate{}, err
		}
		candidates, err := readUpgradePlanCandidates(path)
		if err != nil {
			return upgradePlanCandidate{}, err
		}
		if len(candidates) != 1 {
			return upgradePlanCandidate{}, fmt.Errorf("--plan-file %s contains %d upgrade.cattle.io Plan documents; expected exactly one", path, len(candidates))
		}
		return candidates[0], nil
	}

	var all []upgradePlanCandidate
	err := filepath.WalkDir(filepath.Join(repoRoot, "kubernetes"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml") {
			return nil
		}
		candidates, err := readUpgradePlanCandidates(path)
		if err != nil {
			return err
		}
		all = append(all, candidates...)
		return nil
	})
	if err != nil {
		return upgradePlanCandidate{}, fmt.Errorf("scan Kubernetes manifests for kubeadm Plan: %w", err)
	}
	var kubeadm []upgradePlanCandidate
	for _, candidate := range all {
		if strings.Contains(strings.ToLower(candidate.Name), "kubeadm") {
			kubeadm = append(kubeadm, candidate)
		}
	}
	if len(kubeadm) == 1 {
		return kubeadm[0], nil
	}
	if len(kubeadm) == 0 {
		return upgradePlanCandidate{}, fmt.Errorf("no kubeadm upgrade.cattle.io Plan found under %s", filepath.Join(repoRoot, "kubernetes"))
	}
	sort.Slice(kubeadm, func(i, j int) bool { return kubeadm[i].Path < kubeadm[j].Path })
	items := make([]string, 0, len(kubeadm))
	for _, candidate := range kubeadm {
		rel, _ := filepath.Rel(repoRoot, candidate.Path)
		items = append(items, fmt.Sprintf("%s (%s, %s)", filepath.ToSlash(rel), candidate.Name, candidate.Version))
	}
	return upgradePlanCandidate{}, fmt.Errorf("multiple kubeadm Plans found; choose one with --plan-file:\n  %s", strings.Join(items, "\n  "))
}

func ensurePathWithinRepo(repoRoot, path string) error {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("plan file %s is outside repository root %s", path, repoRoot)
	}
	return nil
}

func readUpgradePlanCandidates(path string) ([]upgradePlanCandidate, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- local manifest discovered below the selected repository root
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	text := string(data)
	if !strings.Contains(text, "upgrade.cattle.io/") || !strings.Contains(text, "Plan") {
		return nil, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var candidates []upgradePlanCandidate
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse candidate Plan manifest %s: %w", path, err)
		}
		if len(document.Content) == 0 {
			continue
		}
		root := document.Content[0]
		api := yamlMappingValue(root, "apiVersion")
		kind := yamlMappingValue(root, "kind")
		if api == nil || kind == nil || !strings.HasPrefix(api.Value, "upgrade.cattle.io/") || kind.Value != "Plan" {
			continue
		}
		metadata := yamlMappingValue(root, "metadata")
		spec := yamlMappingValue(root, "spec")
		name := yamlMappingValue(metadata, "name")
		version := yamlMappingValue(spec, "version")
		if name == nil || version == nil || strings.TrimSpace(name.Value) == "" || strings.TrimSpace(version.Value) == "" {
			return nil, fmt.Errorf("upgrade Plan in %s must contain metadata.name and spec.version", path)
		}
		candidates = append(candidates, upgradePlanCandidate{Path: path, Name: name.Value, Version: version.Value, Line: version.Line})
	}
	return candidates, nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func validateUpgradePlanVersion(target, current string, allowDowngrade bool) (string, error) {
	if !strictKubernetesVersionRE.MatchString(target) {
		return "", fmt.Errorf("version %q must use vX.Y.Z format", target)
	}
	if !strictKubernetesVersionRE.MatchString(current) {
		return "", fmt.Errorf("current Plan version %q must use vX.Y.Z format", current)
	}
	comparison, err := compareKubernetesVersions(target, current)
	if err != nil {
		return "", err
	}
	if comparison < 0 && !allowDowngrade {
		return "", fmt.Errorf("refusing kubeadm Plan downgrade from %s to %s without --allow-downgrade", current, target)
	}
	from, _ := parseKubernetesVersion(current)
	to, _ := parseKubernetesVersion(target)
	if to.Major != from.Major || to.Minor-from.Minor > 1 {
		return fmt.Sprintf("target jumps from %s to %s by more than one Kubernetes minor; review kubeadm version-skew policy", current, target), nil
	}
	return "", nil
}

func editUpgradePlanVersion(content []byte, candidate upgradePlanCandidate, target string) ([]byte, error) {
	if candidate.Line < 1 {
		return nil, fmt.Errorf("kubeadm Plan version has no source line")
	}
	lines := bytes.SplitAfter(content, []byte("\n"))
	if candidate.Line > len(lines) {
		return nil, fmt.Errorf("kubeadm Plan version line %d is outside %s", candidate.Line, candidate.Path)
	}
	line := lines[candidate.Line-1]
	ending := []byte{}
	body := line
	if bytes.HasSuffix(body, []byte("\n")) {
		ending = []byte("\n")
		body = body[:len(body)-1]
	}
	if bytes.HasSuffix(body, []byte("\r")) {
		ending = append([]byte("\r"), ending...)
		body = body[:len(body)-1]
	}
	matches := planVersionLineRE.FindSubmatchIndex(body)
	if matches == nil || string(body[matches[6]:matches[7]]) != candidate.Version {
		return nil, fmt.Errorf("spec.version line %d in %s is not a surgical vX.Y.Z scalar", candidate.Line, candidate.Path)
	}
	changed := make([]byte, 0, len(body)-len(candidate.Version)+len(target)+len(ending))
	changed = append(changed, body[:matches[6]]...)
	changed = append(changed, target...)
	changed = append(changed, body[matches[7]:]...)
	changed = append(changed, ending...)
	result := append([]byte(nil), content...)
	start := 0
	for i := 0; i < candidate.Line-1; i++ {
		start += len(lines[i])
	}
	result = append(result[:start], append(changed, result[start+len(line):]...)...)
	return result, nil
}

func renderUpgradePlanLiveContext(ctx context.Context) string {
	report, err := upgradePlanLiveContextFn(ctx)
	if err != nil {
		return "Live cluster context: unavailable (non-fatal): " + err.Error()
	}
	rows := make([]string, 0, len(report.Nodes))
	for _, node := range report.Nodes {
		rows = append(rows, node.Name+"="+node.KubeletVersion)
	}
	sort.Strings(rows)
	return fmt.Sprintf("Live cluster context: apiserver=%s kubelets=[%s]", report.APIServerVersion, strings.Join(rows, ", "))
}
