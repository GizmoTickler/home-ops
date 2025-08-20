package templates

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed volsync/*.j2
var volsyncTemplates embed.FS

//go:embed talos/*.j2 talos/nodes/*.j2 talos/*.yaml
var talosTemplates embed.FS

//go:embed bootstrap/*
var bootstrapTemplates embed.FS

//go:embed brew/*
var brewTemplates embed.FS

// RenderTemplate renders a Jinja2-style template with environment variables
func RenderVolsyncTemplate(templateName string, env map[string]string) (string, error) {
	templateFile := fmt.Sprintf("volsync/%s", templateName)
	content, err := volsyncTemplates.ReadFile(templateFile)
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
	content, err := volsyncTemplates.ReadFile(templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}
	return string(content), nil
}

// RenderTalosTemplate renders a Jinja2-style Talos template with environment variables
func RenderTalosTemplate(templateName string, env map[string]string) (string, error) {
	content, err := talosTemplates.ReadFile(templateName)
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

// GetTalosTemplate returns the raw Talos template content
func GetTalosTemplate(templateName string) (string, error) {
	content, err := talosTemplates.ReadFile(templateName)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}
	return string(content), nil
}

// RenderBootstrapTemplate renders a Jinja2-style bootstrap template with environment variables
func RenderBootstrapTemplate(templateName string, env map[string]string) (string, error) {
	templateFile := fmt.Sprintf("bootstrap/%s", templateName)
	content, err := bootstrapTemplates.ReadFile(templateFile)
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

// GetBootstrapTemplate returns the content of a bootstrap template
func GetBootstrapTemplate(templateName string) (string, error) {
	templateFile := fmt.Sprintf("bootstrap/%s", templateName)
	content, err := bootstrapTemplates.ReadFile(templateFile)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", templateName, err)
	}
	return string(content), nil
}

// GetBootstrapFile returns the content of a bootstrap file (non-template)
func GetBootstrapFile(fileName string) (string, error) {
	filePath := fmt.Sprintf("bootstrap/%s", fileName)
	content, err := bootstrapTemplates.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", fileName, err)
	}
	return string(content), nil
}

// GetBrewfile returns the content of the embedded Brewfile
func GetBrewfile() (string, error) {
	content, err := brewTemplates.ReadFile("brew/Brewfile")
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