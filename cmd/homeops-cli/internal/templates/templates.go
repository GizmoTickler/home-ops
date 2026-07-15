package templates

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"homeops-cli/internal/config"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/secrets"
	"homeops-cli/internal/template"
)

//go:embed volsync/*.j2
var volsyncTemplates embed.FS

//go:embed talos/nodes/*.yaml talos/*.yaml
var talosTemplates embed.FS

//go:embed bootstrap/*
var bootstrapTemplates embed.FS

//go:embed brew/*
var brewTemplates embed.FS

//go:embed flatcar/butane/*.bu flatcar/kubeadm/*.yaml flatcar/files/* flatcar/manifests/*
var flatcarTemplates embed.FS

// readTemplateFile returns template content, preferring a user override from
// the configured templates.dir (homeops.yaml) over the embedded copy. The
// override file shadows the embedded one by relative path, e.g.
// <templates.dir>/talos/controlplane.yaml.
func readTemplateFile(embedded embed.FS, path string) ([]byte, error) {
	if dir := config.Get().Templates.Dir; dir != "" {
		if expanded, err := secrets.ExpandHome(dir); err == nil {
			cleanPath := filepath.Clean(path)
			if filepath.IsAbs(cleanPath) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(os.PathSeparator)) {
				return nil, fmt.Errorf("template path escapes templates.dir: %s", path)
			}
			if data, err := os.ReadFile(filepath.Join(expanded, cleanPath)); err == nil { // #nosec G304 -- user-configured templates.dir override path validated to stay relative
				return data, nil
			}
		}
	}
	return embedded.ReadFile(path)
}

// RenderTemplate renders a Jinja2-style template with environment variables
func RenderVolsyncTemplate(templateName string, env map[string]string) (string, error) {
	templateFile := fmt.Sprintf("volsync/%s", templateName)
	content, err := readTemplateFile(volsyncTemplates, templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}

	// Simple Jinja2-style variable replacement
	result := string(content)
	for key, value := range env {
		placeholder := fmt.Sprintf("{{ ENV.%s }}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result, nil
}

// GetVolsyncTemplate returns the raw template content
func GetVolsyncTemplate(templateName string) (string, error) {
	templateFile := fmt.Sprintf("volsync/%s", templateName)
	content, err := readTemplateFile(volsyncTemplates, templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}
	return string(content), nil
}

// RenderTalosTemplate renders a Jinja2-style Talos template with environment variables
func RenderTalosTemplate(templateName string, env map[string]string) (string, error) {
	env = enrichTalosEnv(templateName, env)
	content, err := readTemplateFile(talosTemplates, templateName)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}

	// Simple Jinja2-style variable replacement (ORIGINAL IMPLEMENTATION)
	result := string(content)
	for key, value := range env {
		placeholder := fmt.Sprintf("{{ ENV.%s }}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result, nil
}

func enrichTalosEnv(templateName string, env map[string]string) map[string]string {
	merged := map[string]string{}
	for k, v := range talosDefaultEnv(templateName) {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}
	return merged
}

func talosDefaultEnv(templateName string) map[string]string {
	cfg := config.Get()
	out := map[string]string{
		"CLUSTER_NAME":                    cfg.ClusterNameWithDefault(),
		"POD_CIDR":                        cfg.Cluster.PodCIDR,
		"SERVICE_CIDR":                    cfg.Cluster.ServiceCIDR,
		"DNS_DOMAIN":                      cfg.Cluster.DNSDomain,
		"NODE_SUBNET":                     cfg.Cluster.NodeSubnet,
		"TALOS_EXTRA_CERT_SANS_MACHINE":   formatYAMLList(cfg.Cluster.ExtraCertSANs, 4, false),
		"TALOS_EXTRA_CERT_SANS_APISERVER": formatYAMLList(cfg.Cluster.ExtraCertSANs, 6, false),
		"TALOS_NTP_SERVERS":               formatYAMLList(cfg.Cluster.NTPServers, 6, false),
		"TALOS_DISCOVERY_ENDPOINT":        cfg.Cluster.Talos.DiscoveryEndpoint,
		"TALOS_CONTROLPLANE_INSTALL_DISK": cfg.Cluster.Talos.ControlPlaneInstallDisk,
		"TALOS_WORKER_INSTALL_DISK":       cfg.Cluster.Talos.WorkerInstallDisk,
		"TALOS_USER_VOLUME_DISK":          cfg.Cluster.Talos.UserVolume.Disk,
		"TALOS_USER_VOLUME_MIN_SIZE":      cfg.Cluster.Talos.UserVolume.MinSize,
		"TALOS_USER_VOLUME_MAX_SIZE":      cfg.Cluster.Talos.UserVolume.MaxSize,
		"TALOS_KUBERNETES_VERSION":        config.GetVersions(".").TalosKubernetesVersion,
		"TALOS_VERSION":                   config.GetVersions(".").TalosVersion,
	}
	if node, ok := talosNodeForTemplate(templateName, cfg); ok {
		profile := node.VM.ForProvider("talos")
		out["TALOS_NODE_MAC"] = profile.Mac
		out["TALOS_NODE_HOSTNAME"] = node.Name
	}
	return out
}

func talosNodeForTemplate(templateName string, cfg *config.Config) (config.Node, bool) {
	base := filepath.Base(templateName)
	ip := strings.TrimSuffix(base, filepath.Ext(base))
	return cfg.NodeByIP(ip)
}

func formatYAMLList(values []string, indent int, quote bool) string {
	prefix := strings.Repeat(" ", indent) + "- "
	lines := make([]string, 0, len(values))
	for _, v := range values {
		if quote {
			v = fmt.Sprintf("%q", v)
		}
		lines = append(lines, prefix+v)
	}
	return strings.Join(lines, "\n")
}

// GetTalosTemplate returns the raw Talos template content
func GetTalosTemplate(templateName string) (string, error) {
	content, err := readTemplateFile(talosTemplates, templateName)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}
	return string(content), nil
}

// RenderFlatcarTemplate renders a Flatcar template (Butane, kubeadm config, or a
// local: referenced file) with {{ ENV.* }} substitution. The templateName is the path
// relative to the embedded flatcar/ directory, e.g. "butane/controlplane.bu",
// "kubeadm/init-config.yaml", "files/containerd-config.toml", "manifests/kube-vip.yaml".
func RenderFlatcarTemplate(templateName string, env map[string]string) (string, error) {
	templateFile := fmt.Sprintf("flatcar/%s", templateName)
	content, err := readTemplateFile(flatcarTemplates, templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read flatcar template %s: %w", templateName, err)
	}

	// Simple Jinja2-style variable replacement (same as Talos/Volsync renderers).
	result := string(content)
	for key, value := range env {
		placeholder := fmt.Sprintf("{{ ENV.%s }}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result, nil
}

// GetFlatcarTemplate returns the raw Flatcar template content (no substitution).
func GetFlatcarTemplate(templateName string) (string, error) {
	templateFile := fmt.Sprintf("flatcar/%s", templateName)
	content, err := readTemplateFile(flatcarTemplates, templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read flatcar template %s: %w", templateName, err)
	}
	return string(content), nil
}

// ListFlatcarFiles returns the embedded file paths (relative to flatcar/) under the
// given subdirectory (e.g. "files" or "manifests"). Used by the Ignition renderer to
// materialize local:-referenced files into a temp FilesDir.
func ListFlatcarFiles(subdir string) ([]string, error) {
	dir := fmt.Sprintf("flatcar/%s", subdir)
	entries, err := flatcarTemplates.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list flatcar dir %s: %w", subdir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, fmt.Sprintf("%s/%s", subdir, e.Name()))
	}
	return names, nil
}

// RenderBootstrapTemplate renders a Jinja2-style bootstrap template with environment variables
func RenderBootstrapTemplate(templateName string, env map[string]string) (string, error) {
	env = enrichBootstrapEnv(env)
	templateFile := fmt.Sprintf("bootstrap/%s", templateName)
	content, err := readTemplateFile(bootstrapTemplates, templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}

	// Enhanced Jinja2-style template processing
	result := string(content)

	// Handle for loops (simple case for namespaces)
	if strings.Contains(result, "{% for namespace in") {
		result = expandNamespaceLoop(result)
	}

	// Handle environment variable replacement
	for key, value := range env {
		placeholder := fmt.Sprintf("{{ ENV.%s }}", key)
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result, nil
}

func enrichBootstrapEnv(env map[string]string) map[string]string {
	merged := map[string]string{
		"OP_VAULT": config.Get().Bootstrap.OpVault,
	}
	for k, v := range env {
		merged[k] = v
	}
	return merged
}

// GetBootstrapTemplate returns the content of a bootstrap template
func GetBootstrapTemplate(templateName string) (string, error) {
	// Check if this is the values template which is now in a different location
	if templateName == "values.yaml.gotmpl" {
		templateFile := "bootstrap/helmfile.d/templates/values.yaml.gotmpl"
		content, err := readTemplateFile(bootstrapTemplates, templateFile)
		if err != nil {
			return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
		}
		return string(content), nil
	}

	// For other templates, use the standard path
	templateFile := fmt.Sprintf("bootstrap/%s", templateName)
	content, err := readTemplateFile(bootstrapTemplates, templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}
	return string(content), nil
}

// GetBootstrapFile returns the content of a bootstrap file (non-template)
func GetBootstrapFile(fileName string) (string, error) {
	filePath := fmt.Sprintf("bootstrap/%s", fileName)
	content, err := readTemplateFile(bootstrapTemplates, filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", fileName, err)
	}
	return string(content), nil
}

// GetBrewfile returns the content of the embedded Brewfile
func GetBrewfile() (string, error) {
	content, err := readTemplateFile(brewTemplates, "brew/Brewfile")
	if err != nil {
		return "", fmt.Errorf("failed to read Brewfile: %w", err)
	}
	return string(content), nil
}

// expandNamespaceLoop expands the Jinja2 for loop for namespaces
func expandNamespaceLoop(content string) string {
	// Find the for loop pattern
	forPattern := `{% for namespace in ["external-secrets", "flux-system", "network"] %}`
	endPattern := `{% endfor %}`

	forIndex := strings.Index(content, forPattern)
	if forIndex == -1 {
		return content // No for loop found
	}

	endIndex := strings.Index(content[forIndex:], endPattern)
	if endIndex == -1 {
		return content // No matching endfor found
	}
	endIndex += forIndex + len(endPattern)

	// Extract the loop content
	loopStart := forIndex + len(forPattern)
	loopEnd := forIndex + endIndex - len(endPattern)
	loopContent := content[loopStart:loopEnd]

	// Define the namespaces
	namespaces := []string{"external-secrets", "flux-system", "network"}

	// Expand the loop
	var expanded strings.Builder
	for _, namespace := range namespaces {
		expandedContent := strings.ReplaceAll(loopContent, "{{ namespace }}", namespace)
		expanded.WriteString(expandedContent)
	}

	// Replace the entire loop with the expanded content
	result := content[:forIndex] + expanded.String() + content[endIndex:]
	return result
}

// RenderGoTemplate renders a Go template from bootstrap templates with helmfile-style functions
func RenderGoTemplate(templateName, rootDir string, data map[string]interface{}, collector *metrics.PerformanceCollector) (string, error) {
	// Get the template content
	content, err := GetBootstrapTemplate(templateName)
	if err != nil {
		return "", fmt.Errorf("failed to get template %s: %w", templateName, err)
	}

	// Create Go template renderer
	renderer := template.NewGoTemplateRenderer(rootDir, collector)

	// Prepare template data
	templateData := template.TemplateData{
		RootDir: rootDir,
		Values:  data,
	}

	// Render template
	return renderer.RenderTemplate(content, templateData)
}

// RenderHelmfileValues renders dynamic values for a specific release
func RenderHelmfileValues(release, rootDir string, collector *metrics.PerformanceCollector) (string, error) {
	// Get the values template
	valuesTemplate, err := GetBootstrapTemplate("values.yaml.gotmpl")
	if err != nil {
		return "", fmt.Errorf("failed to get values template: %w", err)
	}

	// Create Go template renderer
	renderer := template.NewGoTemplateRenderer(rootDir, collector)

	// Render values for the specific release
	return renderer.RenderHelmfileValues(valuesTemplate, release)
}

// validateTemplateSubstitution verifies that Jinja2 template substitution worked correctly
// by checking the rendered output against expected patterns
func ValidateTemplateSubstitution(templateName, originalTemplate, renderedContent string) error {
	// Check that all Jinja2 syntax has been resolved
	if strings.Contains(renderedContent, "{% for") {
		return fmt.Errorf("template '%s' contains unresolved for loops", templateName)
	}
	if strings.Contains(renderedContent, "{% endfor %}") {
		return fmt.Errorf("template '%s' contains unresolved endfor tags", templateName)
	}
	if strings.Contains(renderedContent, "{{ namespace }}") {
		return fmt.Errorf("template '%s' contains unresolved namespace variables", templateName)
	}
	if strings.Contains(renderedContent, "{{ ENV.") {
		return fmt.Errorf("template '%s' contains unresolved environment variables", templateName)
	}

	// For resources.yaml.j2, verify namespace expansion worked
	if templateName == "resources.yaml.j2" {
		expectedNamespaces := []string{"external-secrets", "flux-system", "network"}
		for _, ns := range expectedNamespaces {
			if !strings.Contains(renderedContent, fmt.Sprintf("name: %s", ns)) {
				return fmt.Errorf("namespace '%s' not found in rendered template - Jinja2 loop expansion failed", ns)
			}
		}
	}

	return nil
}
