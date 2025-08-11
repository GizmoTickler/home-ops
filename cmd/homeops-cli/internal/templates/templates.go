package templates

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed volsync/*.j2
var volsyncTemplates embed.FS

//go:embed talos/*.j2 talos/nodes/*.j2
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
	loopStart := strings.Index(content, "{% for namespace in")
	if loopStart == -1 {
		return content
	}
	
	// Find the end of the for loop
	loopEnd := strings.Index(content[loopStart:], "{% endfor %}")
	if loopEnd == -1 {
		return content
	}
	loopEnd += loopStart + len("{% endfor %}")
	
	// Extract the loop content
	loopContent := content[loopStart:loopEnd]
	
	// Extract the namespaces list
	listStart := strings.Index(loopContent, "[")
	listEnd := strings.Index(loopContent, "]")
	if listStart == -1 || listEnd == -1 {
		return content
	}
	
	namespacesStr := loopContent[listStart+1:listEnd]
	namespaces := strings.Split(namespacesStr, ",")
	
	// Find the template inside the loop
	templateStart := strings.Index(loopContent, "---")
	templateEnd := strings.Index(loopContent, "{% endfor %}")
	if templateStart == -1 || templateEnd == -1 {
		return content
	}
	
	template := loopContent[templateStart:templateEnd]
	
	// Generate the expanded content
	var expanded strings.Builder
	for i, namespace := range namespaces {
		namespace = strings.Trim(strings.TrimSpace(namespace), `"`)
		if namespace == "" {
			continue
		}
		
		expandedTemplate := strings.ReplaceAll(template, "{{ namespace }}", namespace)
		expanded.WriteString(expandedTemplate)
		
		// Add separator between iterations (except for the last one)
		if i < len(namespaces)-1 {
			expanded.WriteString("\n")
		}
	}
	
	// Replace the loop with the expanded content
	result := content[:loopStart] + expanded.String() + content[loopEnd:]
	return result
}