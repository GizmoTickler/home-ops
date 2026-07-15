package kubernetes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"homeops-cli/internal/ui"
)

const diffFieldManager = "homeops-diff"

var kubectlDiffManifestFn = kubectlDiffManifest

type kustomizationDiffReport struct {
	Changed   []string `json:"changed"`
	Added     []string `json:"added"`
	Diff      string   `json:"diff"`
	Unchanged int      `json:"-"`
}

type renderedResource struct {
	DisplayName string
	DiffName    string
}

func newDiffCommand() *cobra.Command {
	var ksName, output string
	cmd := &cobra.Command{
		Use:          "diff [ks.yaml]",
		Short:        "Preview a Flux Kustomization against the live cluster",
		SilenceUsage: true,
		Long: `Locally renders a Flux Kustomization, including the same SOPS and
cluster-config substitutions used by render-ks, then sends the rendered objects
to kubectl diff using server-side dry-run. It never applies resources and uses
the isolated field manager homeops-diff.

The preview is limited to objects emitted by flux build; it does not predict
Flux pruning, controller-generated objects, admission side effects outside the
dry-run response, or changes from sources not available to the local renderer.
Secret values remain masked by kubectl diff's default behavior. With no path,
the command presents discovered kubernetes/apps/**/ks.yaml candidates.`,
		Example: `  homeops-cli k8s diff ./kubernetes/apps/media/plex/ks.yaml
  homeops-cli k8s diff ./kubernetes/apps/observability/grafana/ks.yaml --name grafana-instance
  homeops-cli k8s diff ./kubernetes/apps/media/plex/ks.yaml --output json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ui.ValidateOutputFormat(output); err != nil {
				return err
			}

			ksPath := ""
			if len(args) == 0 {
				selected, selectedName, err := selectKustomizationFile()
				if err != nil || selected == "" {
					return err
				}
				ksPath = selected
				if ksName == "" {
					ksName = selectedName
				}
			} else {
				ksPath = args[0]
			}

			return runKustomizationDiff(cmd.Context(), ksPath, ksName, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&ksName, "name", "", "Kustomization name within a multi-document ks.yaml")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	return cmd
}

func runKustomizationDiff(ctx context.Context, ksPath, ksName, output string, out io.Writer) error {
	_, manifest, err := buildKustomizationManifestOnlineFn(ksPath, ksName)
	if err != nil {
		return err
	}

	diffOutput, err := kubectlDiffManifestFn(ctx, manifest)
	if err != nil {
		return err
	}
	report, err := summarizeKustomizationDiff(manifest, diffOutput)
	if err != nil {
		return fmt.Errorf("summarize kubectl diff: %w", err)
	}

	if output == "json" {
		encoded, err := ui.RenderJSON(report)
		if err != nil {
			return fmt.Errorf("marshal diff report: %w", err)
		}
		_, err = fmt.Fprintln(out, encoded)
		return err
	}

	if _, err := fmt.Fprintf(out, "%d resources changed, %d added, %d unchanged\n", len(report.Changed), len(report.Added), report.Unchanged); err != nil {
		return err
	}
	if report.Diff != "" {
		_, err = io.WriteString(out, report.Diff)
		if err == nil && !strings.HasSuffix(report.Diff, "\n") {
			_, err = io.WriteString(out, "\n")
		}
	}
	return err
}

// kubectlDiffManifest invokes only kubectl's server-side dry-run diff path.
// Exit 1 is the documented success state meaning a diff was produced.
func kubectlDiffManifest(ctx context.Context, manifest string) (string, error) {
	// --force-conflicts is safe here: diff is a server-side DRY-RUN, so
	// overriding field ownership (e.g. flux's kustomize-controller labels)
	// never persists anything.
	cmd := exec.CommandContext(ctx, "kubectl", "diff", "--server-side", "--force-conflicts", "--field-manager="+diffFieldManager, "-f", "-")
	// kubectl otherwise honors KUBECTL_EXTERNAL_DIFF, which could make output
	// non-unified and break both the product contract and resource summary.
	cmd.Env = append(os.Environ(), "KUBECTL_EXTERNAL_DIFF=")
	cmd.Stdin = strings.NewReader(manifest)
	output, err := cmd.CombinedOutput()
	return interpretKubectlDiffResult(output, err)
}

func interpretKubectlDiffResult(output []byte, err error) (string, error) {
	if err == nil {
		return string(output), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return string(output), nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return "", fmt.Errorf("kubectl diff failed: %w", err)
	}
	return "", fmt.Errorf("kubectl diff failed: %w: %s", err, message)
}

func summarizeKustomizationDiff(manifest, diffOutput string) (kustomizationDiffReport, error) {
	resources, err := parseRenderedResources(manifest)
	if err != nil {
		return kustomizationDiffReport{}, err
	}

	byDiffName := make(map[string]renderedResource, len(resources))
	for _, resource := range resources {
		byDiffName[resource.DiffName] = resource
	}

	changedSet := map[string]bool{}
	addedSet := map[string]bool{}
	lines := strings.Split(diffOutput, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "diff ") {
			continue
		}
		fields := strings.Fields(lines[i])
		if len(fields) < 2 {
			continue
		}
		diffName := filepath.Base(fields[len(fields)-1])
		resource, ok := byDiffName[diffName]
		if !ok {
			continue
		}

		added := false
		for j := i + 1; j < len(lines) && !strings.HasPrefix(lines[j], "diff "); j++ {
			if strings.HasPrefix(lines[j], "@@ -0,0 ") || strings.HasPrefix(lines[j], "@@ -0 ") {
				added = true
				break
			}
		}
		if added {
			addedSet[resource.DisplayName] = true
		} else {
			changedSet[resource.DisplayName] = true
		}
	}

	for name := range addedSet {
		delete(changedSet, name)
	}
	changed := sortedSetKeys(changedSet)
	added := sortedSetKeys(addedSet)
	unchanged := len(resources) - len(changed) - len(added)
	if unchanged < 0 {
		unchanged = 0
	}
	return kustomizationDiffReport{Changed: changed, Added: added, Diff: diffOutput, Unchanged: unchanged}, nil
}

func parseRenderedResources(manifest string) ([]renderedResource, error) {
	type manifestObject struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Metadata   struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}

	decoder := yaml.NewDecoder(bytes.NewBufferString(manifest))
	seen := map[string]bool{}
	var resources []renderedResource
	for {
		var object manifestObject
		if err := decoder.Decode(&object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse rendered manifests: %w", err)
		}
		if object.APIVersion == "" || object.Kind == "" || object.Metadata.Name == "" {
			continue
		}
		group, version := splitAPIVersion(object.APIVersion)
		diffName := strings.Join(nonEmptyDiffNameParts(group, version, object.Kind, object.Metadata.Namespace, object.Metadata.Name), ".")
		// kubectl preserves the empty namespace component for cluster-scoped
		// resources: v1.Namespace..name.
		if object.Metadata.Namespace == "" {
			prefix := ""
			if group != "" {
				prefix = group + "."
			}
			diffName = fmt.Sprintf("%s%s.%s..%s", prefix, version, object.Kind, object.Metadata.Name)
		}
		if seen[diffName] {
			continue
		}
		seen[diffName] = true
		display := fmt.Sprintf("%s/%s", object.Kind, object.Metadata.Name)
		if object.Metadata.Namespace != "" {
			display = fmt.Sprintf("%s/%s/%s", object.Kind, object.Metadata.Namespace, object.Metadata.Name)
		}
		resources = append(resources, renderedResource{DisplayName: display, DiffName: diffName})
	}
	return resources, nil
}

func splitAPIVersion(apiVersion string) (string, string) {
	parts := strings.SplitN(apiVersion, "/", 2)
	if len(parts) == 1 {
		return "", parts[0]
	}
	return parts[0], parts[1]
}

func nonEmptyDiffNameParts(parts ...string) []string {
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func sortedSetKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
