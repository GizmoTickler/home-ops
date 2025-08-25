package templates

import (
	"fmt"
	"strings"

	"homeops-cli/internal/common"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/template"
	"homeops-cli/internal/yaml"
)

// TemplateRenderer provides a unified interface for different template engines
type TemplateRenderer struct {
	rootDir    string
	logger     *common.ColorLogger
	metrics    *metrics.PerformanceCollector
	goRenderer *template.GoTemplateRenderer
}

// NewTemplateRenderer creates a new unified template renderer
func NewTemplateRenderer(rootDir string, logger *common.ColorLogger, metrics *metrics.PerformanceCollector) *TemplateRenderer {
	return &TemplateRenderer{
		rootDir:    rootDir,
		logger:     logger,
		metrics:    metrics,
		goRenderer: template.NewGoTemplateRenderer(rootDir, metrics),
	}
}

// RenderTemplate automatically selects the appropriate template engine based on content
func (r *TemplateRenderer) RenderTemplate(templateName, content string, env map[string]string, data map[string]interface{}) (string, error) {
	// Determine template type based on content or filename
	switch {
	case strings.Contains(templateName, ".gotmpl") || strings.Contains(content, "{{"):
		// Use Go template renderer
		templateData := template.TemplateData{
			RootDir: r.rootDir,
			Values:  data,
		}
		return r.goRenderer.RenderTemplate(content, templateData)

	case strings.Contains(templateName, ".j2") || strings.Contains(content, "{%") || strings.Contains(content, "{{ ENV."):
		// Use Jinja2 renderer for Talos templates
		if strings.HasPrefix(templateName, "talos/") {
			return RenderTalosTemplate(strings.TrimPrefix(templateName, "talos/"), env)
		} else if strings.HasPrefix(templateName, "volsync/") {
			return RenderVolsyncTemplate(strings.TrimPrefix(templateName, "volsync/"), env)
		}
		// Fallback to bootstrap template for other Jinja2 templates
		return RenderBootstrapTemplate(templateName, env)

	default:
		// Simple variable replacement for basic templates
		result := content
		for key, value := range env {
			placeholder := fmt.Sprintf("{{ ENV.%s }}", key)
			result = strings.ReplaceAll(result, placeholder, value)
		}
		return result, nil
	}
}

// RenderTalosConfigWithMerge renders and merges Talos configurations with proper 1Password resolution
func (r *TemplateRenderer) RenderTalosConfigWithMerge(baseTemplate, patchTemplate string, env map[string]string) ([]byte, error) {
	// Get base config
	baseConfig, err := RenderTalosTemplate(baseTemplate, env)
	if err != nil {
		return nil, fmt.Errorf("failed to render base template %s: %w", baseTemplate, err)
	}

	// Get patch config
	patchConfig, err := RenderTalosTemplate(patchTemplate, env)
	if err != nil {
		return nil, fmt.Errorf("failed to render patch template %s: %w", patchTemplate, err)
	}

	// Trim leading document separator if present
	patchConfigTrimmed := strings.TrimPrefix(patchConfig, "---\n")
	patchConfigTrimmed = strings.TrimPrefix(patchConfigTrimmed, "---\r\n")

	// Now split by document separators
	var patchParts []string
	if strings.Contains(patchConfigTrimmed, "\n---\n") {
		patchParts = strings.Split(patchConfigTrimmed, "\n---\n")
	} else if strings.Contains(patchConfigTrimmed, "\n---") {
		patchParts = strings.Split(patchConfigTrimmed, "\n---")
	} else {
		patchParts = []string{patchConfigTrimmed}
	}

	// The first part should be the machine config
	machineConfigPatch := strings.TrimSpace(patchParts[0])

	// Ensure the machine config patch starts with proper YAML
	if !strings.HasPrefix(machineConfigPatch, "machine:") && !strings.HasPrefix(machineConfigPatch, "version:") {
		return nil, fmt.Errorf("machine config patch does not start with valid Talos config")
	}

	// Ensure proper Talos config structure
	if !strings.Contains(machineConfigPatch, "version:") {
		machineConfigPatch = "version: v1alpha1\n" + machineConfigPatch
	}

	// Use talosctl for merging (following proven patterns from bootstrap.go)
	mergedConfig, err := r.mergeConfigsWithTalosctl([]byte(baseConfig), []byte(machineConfigPatch))
	if err != nil {
		return nil, fmt.Errorf("failed to merge configs: %w", err)
	}

	// Handle additional YAML documents if present
	var finalConfig string
	if len(patchParts) > 1 {
		// Collect additional documents (skip the first one which is the machine config)
		additionalDocs := patchParts[1:]
		additionalParts := strings.Join(additionalDocs, "\n---\n")
		finalConfig = string(mergedConfig) + "\n---\n" + additionalParts
	} else {
		finalConfig = string(mergedConfig)
	}

	return []byte(finalConfig), nil
}

// mergeConfigsWithTalosctl is a helper method extracted from bootstrap.go
func (r *TemplateRenderer) mergeConfigsWithTalosctl(baseConfig, patchConfig []byte) ([]byte, error) {
	// For now, use the existing YAML processor approach from the original talos.go
	// This maintains backward compatibility while we transition to the unified renderer
	yamlProcessor := yaml.NewProcessor(nil, r.metrics)
	return yamlProcessor.MergeYAMLMultiDocument(baseConfig, patchConfig)
}

// ValidateTemplate validates template syntax based on template type
func (r *TemplateRenderer) ValidateTemplate(templateName, content string) error {
	switch {
	case strings.Contains(templateName, ".gotmpl") || strings.Contains(content, "{{"):
		return r.goRenderer.ValidateTemplate(content)
	case strings.Contains(content, "{%") || strings.Contains(content, "{{ ENV."):
		// For Jinja2 templates, we can try rendering with empty context
		_, err := r.RenderTemplate(templateName, content, make(map[string]string), make(map[string]interface{}))
		return err
	default:
		// Basic templates are always valid
		return nil
	}
}
