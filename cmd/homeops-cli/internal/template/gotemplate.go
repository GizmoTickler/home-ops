package template

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"

	"homeops-cli/internal/errors"
	"homeops-cli/internal/metrics"
)

// GoTemplateRenderer handles Go template rendering with helmfile-style functions
type GoTemplateRenderer struct {
	rootDir string
	metrics *metrics.PerformanceCollector
}

// NewGoTemplateRenderer creates a new Go template renderer
func NewGoTemplateRenderer(rootDir string, collector *metrics.PerformanceCollector) *GoTemplateRenderer {
	return &GoTemplateRenderer{
		rootDir: rootDir,
		metrics: collector,
	}
}

// TemplateData represents data available to templates
type TemplateData struct {
	RootDir string
	Values  map[string]interface{}
}

// RenderTemplate renders a Go template with helmfile-style functions
func (r *GoTemplateRenderer) RenderTemplate(templateContent string, data TemplateData) (string, error) {
	result, err := r.metrics.TrackOperationWithResult("gotemplate_render", func() (interface{}, error) {
		// Create template with custom functions
		tmpl := template.New("template").Funcs(r.createTemplateFuncs(data.RootDir))
		
		// Parse template
		tmpl, err := tmpl.Parse(templateContent)
		if err != nil {
			return nil, errors.NewTemplateError("PARSE_FAILED", "Failed to parse Go template", err)
		}

		// Execute template
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return nil, errors.NewTemplateError("EXECUTE_FAILED", "Failed to execute Go template", err)
		}

		return buf.String(), nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// RenderFile renders a Go template file
func (r *GoTemplateRenderer) RenderFile(templatePath string, data TemplateData) (string, error) {
	result, err := r.metrics.TrackOperationWithResult("gotemplate_render_file", func() (interface{}, error) {
		content, err := os.ReadFile(templatePath)
		if err != nil {
			return nil, errors.NewTemplateError("READ_FAILED", fmt.Sprintf("Failed to read template file: %s", templatePath), err)
		}

		return r.RenderTemplate(string(content), data)
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// createTemplateFuncs creates custom template functions similar to helmfile
func (r *GoTemplateRenderer) createTemplateFuncs(rootDir string) template.FuncMap {
	return template.FuncMap{
		"exec": func(command string, args []interface{}) (string, error) {
			return r.execCommand(command, args)
		},
		"list": func(items ...interface{}) []interface{} {
			return items
		},
		"printf": func(format string, args ...interface{}) string {
			return fmt.Sprintf(format, args...)
		},
		"indent": func(spaces int, text string) string {
			return r.indentText(spaces, text)
		},
		"trim": strings.TrimSpace,
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"replace": func(old, new, s string) string {
			return strings.ReplaceAll(s, old, new)
		},
	}
}

// execCommand executes a command with arguments (similar to helmfile's exec function)
func (r *GoTemplateRenderer) execCommand(command string, args []interface{}) (string, error) {
	// Convert args to strings
	stringArgs := make([]string, len(args))
	for i, arg := range args {
		stringArgs[i] = fmt.Sprintf("%v", arg)
	}

	// Execute command
	cmd := exec.Command(command, stringArgs...)
	cmd.Dir = r.rootDir // Set working directory
	
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute command '%s %v': %w", command, stringArgs, err)
	}

	return strings.TrimSpace(string(output)), nil
}

// indentText indents text by the specified number of spaces
func (r *GoTemplateRenderer) indentText(spaces int, text string) string {
	if spaces == 0 {
		return text
	}
	
	indent := strings.Repeat(" ", spaces)
	lines := strings.Split(text, "\n")
	
	for i, line := range lines {
		if strings.TrimSpace(line) != "" { // Don't indent empty lines
			lines[i] = indent + line
		}
	}
	
	return strings.Join(lines, "\n")
}

// ValidateTemplate validates a Go template without executing it
func (r *GoTemplateRenderer) ValidateTemplate(templateContent string) error {
	return r.metrics.TrackOperation("gotemplate_validate", func() error {
		tmpl := template.New("validate").Funcs(r.createTemplateFuncs(r.rootDir))
		_, err := tmpl.Parse(templateContent)
		if err != nil {
			return errors.NewTemplateError("VALIDATION_FAILED", "Go template validation failed", err)
		}
		return nil
	})
}

// GetTemplateVariables extracts variable names from a Go template
func (r *GoTemplateRenderer) GetTemplateVariables(templateContent string) ([]string, error) {
	result, err := r.metrics.TrackOperationWithResult("gotemplate_get_variables", func() (interface{}, error) {
		// This is a simplified approach - for full variable extraction,
		// you'd need to parse the template AST
		variables := make([]string, 0)
		
		// Look for common variable patterns
		lines := strings.Split(templateContent, "\n")
		for _, line := range lines {
			// Look for .Variable patterns
			if strings.Contains(line, ".Values") {
				variables = append(variables, "Values")
			}
			if strings.Contains(line, ".RootDir") {
				variables = append(variables, "RootDir")
			}
		}
		
		return variables, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]string), nil
}

// ReleaseInfo contains information about a Helm release
type ReleaseInfo struct {
	Name      string
	Namespace string
}

// RenderHelmfileValues renders helmfile values using the dynamic template
func (r *GoTemplateRenderer) RenderHelmfileValues(valuesTemplate string, release string) (string, error) {
	// Find the namespace for this release by searching the apps directory
	namespace, err := r.findReleaseNamespace(release)
	if err != nil {
		return "", fmt.Errorf("failed to find namespace for release %s: %w", release, err)
	}

	data := TemplateData{
		RootDir: r.rootDir,
		Values: map[string]interface{}{
			"release": release, // Keep the original release string for backward compatibility
		},
	}
	
	// Add Release directly to the template data so it can be accessed as .Release
	releaseInfo := ReleaseInfo{
		Name:      release,
		Namespace: namespace,
	}
	
	// Use a custom data structure that includes Release at the top level
	customData := map[string]interface{}{
		"RootDir": r.rootDir,
		"Values":  data.Values,
		"Release": releaseInfo,
	}
	
	return r.renderTemplateWithCustomData(valuesTemplate, customData)
}

// renderTemplateWithCustomData renders a Go template with custom data structure
func (r *GoTemplateRenderer) renderTemplateWithCustomData(templateContent string, data interface{}) (string, error) {
	result, err := r.metrics.TrackOperationWithResult("gotemplate_render_custom", func() (interface{}, error) {
		// Create template with custom functions - we need to pass the root dir from the data
		var rootDir string
		if dataMap, ok := data.(map[string]interface{}); ok {
			if rd, ok := dataMap["RootDir"].(string); ok {
				rootDir = rd
			} else {
				rootDir = r.rootDir
			}
		} else {
			rootDir = r.rootDir
		}
		
		tmpl := template.New("template").Funcs(r.createTemplateFuncs(rootDir))
		
		// Parse template
		tmpl, err := tmpl.Parse(templateContent)
		if err != nil {
			return nil, errors.NewTemplateError("PARSE_FAILED", "Failed to parse Go template", err)
		}

		// Execute template
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return nil, errors.NewTemplateError("EXECUTE_FAILED", "Failed to execute Go template", err)
		}

		return buf.String(), nil
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// findReleaseNamespace finds the namespace for a given release by searching the apps directory
func (r *GoTemplateRenderer) findReleaseNamespace(releaseName string) (string, error) {
	appsDir := fmt.Sprintf("%s/kubernetes/apps", r.rootDir)
	
	// Walk through all namespace directories
	namespaceEntries, err := os.ReadDir(appsDir)
	if err != nil {
		return "", fmt.Errorf("failed to read apps directory: %w", err)
	}
	
	for _, namespaceEntry := range namespaceEntries {
		if !namespaceEntry.IsDir() {
			continue
		}
		
		namespace := namespaceEntry.Name()
		namespacePath := fmt.Sprintf("%s/%s", appsDir, namespace)
		
		// Check if this namespace contains the release
		releaseEntries, err := os.ReadDir(namespacePath)
		if err != nil {
			continue // Skip if we can't read this directory
		}
		
		for _, releaseEntry := range releaseEntries {
			if !releaseEntry.IsDir() {
				continue
			}
			
			if releaseEntry.Name() == releaseName {
				// Found the release in this namespace
				helmreleasePath := fmt.Sprintf("%s/%s/app/helmrelease.yaml", namespacePath, releaseName)
				if _, err := os.Stat(helmreleasePath); err == nil {
					return namespace, nil
				}
			}
		}
	}
	
	return "", fmt.Errorf("release %s not found in any namespace", releaseName)
}