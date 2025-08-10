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

	// Simple Jinja2-style variable replacement
	result := string(content)
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