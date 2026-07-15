package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/ui"
)

type fluxTreeMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type fluxDependencyRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type fluxKustomization struct {
	Metadata fluxTreeMetadata `json:"metadata"`
	Spec     struct {
		DependsOn       []fluxDependencyRef `json:"dependsOn"`
		Suspend         bool                `json:"suspend"`
		TargetNamespace string              `json:"targetNamespace"`
	} `json:"spec"`
	Status struct {
		Conditions          []conditionJSON `json:"conditions"`
		LastAppliedRevision string          `json:"lastAppliedRevision"`
	} `json:"status"`
}

type fluxKustomizationList struct {
	Items []fluxKustomization `json:"items"`
}

type fluxHelmRelease struct {
	Metadata fluxTreeMetadata `json:"metadata"`
	Spec     struct {
		Suspend bool `json:"suspend"`
	} `json:"spec"`
	Status struct {
		Conditions          []conditionJSON `json:"conditions"`
		LastAppliedRevision string          `json:"lastAppliedRevision"`
	} `json:"status"`
}

type fluxHelmReleaseList struct {
	Items []fluxHelmRelease `json:"items"`
}

type fluxTreeHelmNode struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Ready     string `json:"ready"`
	Suspended bool   `json:"suspended"`
	Revision  string `json:"last_applied_revision,omitempty"`
	Message   string `json:"message,omitempty"`
}

type fluxTreeNode struct {
	Name         string             `json:"name"`
	Namespace    string             `json:"namespace"`
	Ready        string             `json:"ready"`
	Suspended    bool               `json:"suspended"`
	Revision     string             `json:"last_applied_revision,omitempty"`
	Message      string             `json:"message,omitempty"`
	Missing      bool               `json:"missing,omitempty"`
	Dependencies []string           `json:"dependencies"`
	HelmReleases []fluxTreeHelmNode `json:"helm_releases"`
}

type fluxTreeReport struct {
	Root          string                  `json:"root"`
	Nodes         map[string]fluxTreeNode `json:"nodes"`
	Cycles        []string                `json:"cycles,omitempty"`
	BlockingChain []string                `json:"blocking_chain,omitempty"`
}

type fluxKustomizationSummary struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Ready     string `json:"ready"`
	Suspended bool   `json:"suspended"`
	Revision  string `json:"last_applied_revision,omitempty"`
	Message   string `json:"message,omitempty"`
}

func newFluxTreeCommand() *cobra.Command {
	var namespace, output string
	var includeAll bool
	cmd := &cobra.Command{
		Use:          "flux-tree [kustomization-name]",
		Short:        "Trace Flux Kustomization dependencies and blockers",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		Example: `  homeops-cli k8s flux-tree
  homeops-cli k8s flux-tree radarr
  homeops-cli k8s flux-tree rook-ceph-cluster --namespace flux-system --all
  homeops-cli k8s flux-tree radarr --output json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "table" && output != "json" {
				return fmt.Errorf("unsupported output format %q (table, json)", output)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kubernetesDefaultCommandTimeout)
			defer cancel()
			var kustomizations fluxKustomizationList
			if err := kubectlGetJSONContext(ctx, "", fluxKustomizationResource, &kustomizations); err != nil {
				return err
			}
			if len(args) == 0 {
				rendered, err := renderFluxKustomizationList(summarizeKustomizations(kustomizations.Items, namespace), output)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
				return nil
			}
			rootNamespace, err := resolveFluxRootNamespace(kustomizations.Items, namespace, args[0])
			if err != nil {
				return err
			}
			var releases fluxHelmReleaseList
			if err := kubectlGetJSONContext(ctx, "", fluxHelmReleaseResource, &releases); err != nil {
				return err
			}
			report, err := buildFluxTree(kustomizations.Items, releases.Items, rootNamespace, args[0])
			if err != nil {
				return err
			}
			rendered, err := renderFluxTree(report, output, includeAll)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace of the root Kustomization (default: search all namespaces)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().BoolVar(&includeAll, "all", false, "include ready HelmReleases")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

// resolveFluxRootNamespace picks the namespace for the named root
// Kustomization: an explicit --namespace wins; otherwise the name is searched
// across all namespaces and must be unambiguous.
func resolveFluxRootNamespace(items []fluxKustomization, namespace, name string) (string, error) {
	if namespace != "" {
		return namespace, nil
	}
	var matches []string
	for _, item := range items {
		if item.Metadata.Name == name {
			matches = append(matches, item.Metadata.Namespace)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("flux Kustomization %q not found in any namespace", name)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("flux Kustomization %q exists in multiple namespaces (%s) — pick one with --namespace", name, strings.Join(matches, ", "))
	}
}

func summarizeKustomizations(items []fluxKustomization, namespace string) []fluxKustomizationSummary {
	var summaries []fluxKustomizationSummary
	for _, item := range items {
		if namespace != "" && item.Metadata.Namespace != namespace {
			continue
		}
		ready, message := fluxReady(item.Status.Conditions)
		summaries = append(summaries, fluxKustomizationSummary{Name: item.Metadata.Name, Namespace: item.Metadata.Namespace,
			Ready: ready, Suspended: item.Spec.Suspend, Revision: item.Status.LastAppliedRevision, Message: message})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].Name < summaries[j].Name })
	return summaries
}

func buildFluxTree(kustomizations []fluxKustomization, releases []fluxHelmRelease, namespace, rootName string) (fluxTreeReport, error) {
	objects := make(map[string]fluxKustomization, len(kustomizations))
	for _, item := range kustomizations {
		objects[fluxObjectKey(item.Metadata.Namespace, item.Metadata.Name)] = item
	}
	rootKey := fluxObjectKey(namespace, rootName)
	if _, ok := objects[rootKey]; !ok {
		return fluxTreeReport{}, fmt.Errorf("flux Kustomization %s not found", rootKey)
	}
	report := fluxTreeReport{Root: rootKey, Nodes: map[string]fluxTreeNode{}}
	colors := map[string]int{}
	var walk func(string, []string)
	walk = func(key string, path []string) {
		if colors[key] == 1 {
			cycle := append(append([]string{}, path...), key)
			report.Cycles = append(report.Cycles, strings.Join(cycle, " -> "))
			return
		}
		if colors[key] == 2 {
			return
		}
		item, exists := objects[key]
		if !exists {
			namespace, name := splitFluxObjectKey(key)
			report.Nodes[key] = fluxTreeNode{Name: name, Namespace: namespace, Ready: "False", Missing: true, Message: "dependency not found"}
			colors[key] = 2
			return
		}
		colors[key] = 1
		ready, message := fluxReady(item.Status.Conditions)
		node := fluxTreeNode{Name: item.Metadata.Name, Namespace: item.Metadata.Namespace, Ready: ready,
			Suspended: item.Spec.Suspend, Revision: item.Status.LastAppliedRevision, Message: message}
		for _, dependency := range item.Spec.DependsOn {
			dependencyNamespace := dependency.Namespace
			if dependencyNamespace == "" {
				dependencyNamespace = item.Metadata.Namespace
			}
			node.Dependencies = append(node.Dependencies, fluxObjectKey(dependencyNamespace, dependency.Name))
		}
		sort.Strings(node.Dependencies)
		node.HelmReleases = matchingHelmReleases(item, releases)
		report.Nodes[key] = node
		for _, dependency := range node.Dependencies {
			walk(dependency, append(path, key))
		}
		colors[key] = 2
	}
	walk(rootKey, nil)
	sort.Strings(report.Cycles)
	report.BlockingChain = deepestBlockingChain(report)
	return report, nil
}

func matchingHelmReleases(item fluxKustomization, releases []fluxHelmRelease) []fluxTreeHelmNode {
	targetNamespace := item.Spec.TargetNamespace
	if targetNamespace == "" {
		targetNamespace = item.Metadata.Namespace
	}
	var result []fluxTreeHelmNode
	for _, release := range releases {
		labels := release.Metadata.Labels
		if release.Metadata.Namespace != targetNamespace || labels["kustomize.toolkit.fluxcd.io/name"] != item.Metadata.Name {
			continue
		}
		if ownerNamespace := labels["kustomize.toolkit.fluxcd.io/namespace"]; ownerNamespace != "" && ownerNamespace != item.Metadata.Namespace {
			continue
		}
		ready, message := fluxReady(release.Status.Conditions)
		result = append(result, fluxTreeHelmNode{Name: release.Metadata.Name, Namespace: release.Metadata.Namespace,
			Ready: ready, Suspended: release.Spec.Suspend, Revision: release.Status.LastAppliedRevision, Message: message})
	}
	sort.Slice(result, func(i, j int) bool {
		return namespacedName(result[i].Namespace, result[i].Name) < namespacedName(result[j].Namespace, result[j].Name)
	})
	return result
}

func fluxReady(conditions []conditionJSON) (string, string) {
	condition, ok := readyCondition(conditions)
	if !ok {
		return "Unknown", "Ready condition missing"
	}
	if condition.Status == "True" {
		return "True", ""
	}
	return condition.Status, conditionDetail(condition)
}

func deepestBlockingChain(report fluxTreeReport) []string {
	var best []string
	visiting := map[string]bool{}
	var walk func(string, []string)
	walk = func(key string, path []string) {
		if visiting[key] {
			return
		}
		node, ok := report.Nodes[key]
		if !ok {
			return
		}
		visiting[key] = true
		path = append(path, key)
		if node.Ready != "True" || node.Suspended || node.Missing {
			if len(path) > len(best) {
				best = append([]string{}, path...)
			}
		}
		for _, dependency := range node.Dependencies {
			walk(dependency, path)
		}
		delete(visiting, key)
	}
	walk(report.Root, nil)
	for left, right := 0, len(best)-1; left < right; left, right = left+1, right-1 {
		best[left], best[right] = best[right], best[left]
	}
	return best
}

func renderFluxKustomizationList(summaries []fluxKustomizationSummary, output string) (string, error) {
	if output == "json" {
		raw, err := json.MarshalIndent(summaries, "", "  ")
		return string(raw), err
	}
	var rows [][]string
	for _, summary := range summaries {
		rows = append(rows, []string{fluxGlyph(summary.Ready, summary.Suspended, false), summary.Name,
			summary.Ready, strconvBool(summary.Suspended), summary.Revision, summary.Message})
	}
	return ui.Table([]string{"", "KUSTOMIZATION", "READY", "SUSPENDED", "REVISION", "MESSAGE"}, rows), nil
}

func renderFluxTree(report fluxTreeReport, output string, includeAll bool) (string, error) {
	if output == "json" {
		jsonReport := report
		if !includeAll {
			jsonReport.Nodes = make(map[string]fluxTreeNode, len(report.Nodes))
			for key, node := range report.Nodes {
				filtered := node
				filtered.HelmReleases = nil
				for _, release := range node.HelmReleases {
					if release.Ready != "True" || release.Suspended {
						filtered.HelmReleases = append(filtered.HelmReleases, release)
					}
				}
				jsonReport.Nodes[key] = filtered
			}
		}
		raw, err := json.MarshalIndent(jsonReport, "", "  ")
		return string(raw), err
	}
	if output != "" && output != "table" {
		return "", fmt.Errorf("unsupported output format %q (table, json)", output)
	}
	var b strings.Builder
	seen := map[string]bool{}
	var renderNode func(string, string, bool, bool, map[string]bool)
	renderNode = func(key, prefix string, last, root bool, ancestors map[string]bool) {
		node := report.Nodes[key]
		connector := ""
		if !root {
			if last {
				connector = "└── "
			} else {
				connector = "├── "
			}
		}
		fmt.Fprintf(&b, "%s%s%s %s\n", prefix, connector, fluxGlyph(node.Ready, node.Suspended, node.Missing), fluxNodeDetail(node))
		if ancestors[key] {
			fmt.Fprintf(&b, "%s    ↻ cycle to %s\n", prefix, key)
			return
		}
		if seen[key] {
			fmt.Fprintf(&b, "%s    ↳ already shown\n", prefix)
			return
		}
		seen[key] = true
		nextAncestors := cloneStringBoolMap(ancestors)
		nextAncestors[key] = true
		children := append([]string{}, node.Dependencies...)
		helmChildren := make([]fluxTreeHelmNode, 0, len(node.HelmReleases))
		for _, release := range node.HelmReleases {
			if includeAll || release.Ready != "True" || release.Suspended {
				helmChildren = append(helmChildren, release)
			}
		}
		childPrefix := prefix
		if !root {
			if last {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
		}
		for i, dependency := range children {
			isLast := i == len(children)-1 && len(helmChildren) == 0
			renderNode(dependency, childPrefix, isLast, false, nextAncestors)
		}
		for i, release := range helmChildren {
			connector := "├── "
			if i == len(helmChildren)-1 {
				connector = "└── "
			}
			fmt.Fprintf(&b, "%s%s%s HelmRelease %s [%s]", childPrefix, connector,
				fluxGlyph(release.Ready, release.Suspended, false), namespacedName(release.Namespace, release.Name), fluxState(release.Ready, release.Suspended))
			if release.Revision != "" {
				fmt.Fprintf(&b, " revision=%s", release.Revision)
			}
			if release.Message != "" {
				fmt.Fprintf(&b, " — %s", release.Message)
			}
			b.WriteByte('\n')
		}
	}
	renderNode(report.Root, "", true, true, map[string]bool{})
	b.WriteString("\nBLOCKING ANALYSIS: ")
	if len(report.Cycles) > 0 {
		fmt.Fprintf(&b, "cycle detected: %s", strings.Join(report.Cycles, "; "))
	} else if len(report.BlockingChain) == 0 {
		b.WriteString("no blocking Kustomization dependencies found")
	} else {
		root := report.Nodes[report.Root]
		cause := report.Nodes[report.BlockingChain[0]]
		fmt.Fprintf(&b, "%s blocked by: %s", root.Name, fluxBlockingDetail(cause))
		for _, key := range report.BlockingChain[1:] {
			fmt.Fprintf(&b, " <- %s", report.Nodes[key].Name)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func fluxNodeDetail(node fluxTreeNode) string {
	detail := fmt.Sprintf("Kustomization %s [%s]", namespacedName(node.Namespace, node.Name), fluxState(node.Ready, node.Suspended))
	if node.Revision != "" {
		detail += " revision=" + node.Revision
	}
	if node.Message != "" && node.Ready != "True" {
		detail += " — " + node.Message
	}
	return detail
}

func fluxBlockingDetail(node fluxTreeNode) string {
	detail := node.Name + " (Ready=" + node.Ready
	if node.Suspended {
		detail += ", suspended"
	}
	if node.Message != "" {
		detail += ": " + node.Message
	}
	return detail + ")"
}

func fluxState(ready string, suspended bool) string {
	if suspended {
		return "Suspended"
	}
	return "Ready=" + ready
}

func fluxGlyph(ready string, suspended, missing bool) string {
	switch {
	case missing:
		return "!"
	case suspended:
		return "⏸"
	case ready == "True":
		return "✓"
	case ready == "False":
		return "✗"
	default:
		return "?"
	}
}

func fluxObjectKey(namespace, name string) string { return namespacedName(namespace, name) }

func splitFluxObjectKey(key string) (string, string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) == 1 {
		return "", parts[0]
	}
	return parts[0], parts[1]
}

func cloneStringBoolMap(source map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func strconvBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
