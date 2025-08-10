package template

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/1Password/connect-sdk-go/connect"
	"github.com/flosch/pongo2/v6"
	"homeops-cli/internal/errors"
	"homeops-cli/internal/metrics"
)

// Renderer handles template rendering with secret injection
type Renderer struct {
	opClient connect.Client
	vault    string
	metrics  *metrics.PerformanceCollector
}

// RendererConfig holds configuration for the renderer
type RendererConfig struct {
	OnePasswordToken string
	OnePasswordVault string
	ServerURL        string
}

// NewRenderer creates a new template renderer with 1Password integration
func NewRenderer(config RendererConfig, collector *metrics.PerformanceCollector) (*Renderer, error) {
	if config.OnePasswordToken == "" {
		return nil, errors.NewTemplateError("MISSING_TOKEN", "1Password token is required", nil)
	}

	if config.OnePasswordVault == "" {
		return nil, errors.NewTemplateError("MISSING_VAULT", "1Password vault is required", nil)
	}

	client := connect.NewClient(config.ServerURL, config.OnePasswordToken)

	return &Renderer{
		opClient: client,
		vault:    config.OnePasswordVault,
		metrics:  collector,
	}, nil
}

// RenderFile renders a template file with variables and secret injection
func (r *Renderer) RenderFile(templatePath string, vars map[string]interface{}) ([]byte, error) {
	result, err := r.metrics.TrackOperationWithResult("template_render_file", func() (interface{}, error) {
		// Load template from file
		tpl, err := pongo2.FromFile(templatePath)
		if err != nil {
			return nil, errors.NewTemplateError("LOAD_FAILED", fmt.Sprintf("Failed to load template: %s", templatePath), err)
		}

		// Render template with variables
		rendered, err := tpl.Execute(pongo2.Context(vars))
		if err != nil {
			return nil, errors.NewTemplateError("RENDER_FAILED", "Template rendering failed", err)
		}

		// Inject secrets
		return r.injectSecrets([]byte(rendered))
	})
	if err != nil {
		return nil, err
	}
	return result.([]byte), nil
}

// RenderString renders a template string with variables and secret injection
func (r *Renderer) RenderString(templateContent string, vars map[string]interface{}) ([]byte, error) {
	result, err := r.metrics.TrackOperationWithResult("template_render_string", func() (interface{}, error) {
		// Parse template from string
		tpl, err := pongo2.FromString(templateContent)
		if err != nil {
			return nil, errors.NewTemplateError("PARSE_FAILED", "Failed to parse template string", err)
		}

		// Render template with variables
		rendered, err := tpl.Execute(pongo2.Context(vars))
		if err != nil {
			return nil, errors.NewTemplateError("RENDER_FAILED", "Template rendering failed", err)
		}

		// Inject secrets
		return r.injectSecrets([]byte(rendered))
	})
	if err != nil {
		return nil, err
	}
	return result.([]byte), nil
}

// RenderToFile renders a template and writes the result to a file
func (r *Renderer) RenderToFile(templatePath, outputPath string, vars map[string]interface{}) error {
	return r.metrics.TrackOperation("template_render_to_file", func() error {
		result, err := r.RenderFile(templatePath, vars)
		if err != nil {
			return err
		}

		return os.WriteFile(outputPath, result, 0644)
	})
}

// injectSecrets replaces op:// references with actual secrets from 1Password
func (r *Renderer) injectSecrets(content []byte) ([]byte, error) {
	result, err := r.metrics.TrackOperationWithResult("secret_injection", func() (interface{}, error) {
		// Regex to match op://vault/item/field patterns
		opRegex := regexp.MustCompile(`op://([^/]+)/([^/]+)/([^\s"']+)`)
		result := content

		// Find all op:// references
		matches := opRegex.FindAllSubmatch(content, -1)
		if len(matches) == 0 {
			return result, nil // No secrets to inject
		}

		// Process each secret reference
		for _, match := range matches {
			fullMatch := string(match[0])
			vault := string(match[1])
			item := string(match[2])
			field := string(match[3])

			// Get secret from 1Password
			secret, err := r.getSecret(vault, item, field)
			if err != nil {
				return nil, errors.NewTemplateError("SECRET_INJECTION_FAILED", 
					fmt.Sprintf("Failed to inject secret %s", fullMatch), err)
			}

			// Replace the op:// reference with the actual secret
			result = bytes.ReplaceAll(result, match[0], []byte(secret))
		}

		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]byte), nil
}

// getSecret retrieves a secret from 1Password
func (r *Renderer) getSecret(vault, itemTitle, fieldLabel string) (string, error) {
	result, err := r.metrics.TrackOperationWithResult("get_secret", func() (interface{}, error) {
		// Get item from 1Password
		item, err := r.opClient.GetItemByTitle(vault, itemTitle)
		if err != nil {
			return "", fmt.Errorf("failed to get item '%s' from vault '%s': %w", itemTitle, vault, err)
		}

		// Find the field in main fields
		for _, field := range item.Fields {
			if field.Label == fieldLabel {
				return field.Value, nil
			}
		}

		return "", fmt.Errorf("field '%s' not found in item '%s'", fieldLabel, itemTitle)
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// ValidateTemplate checks if a template is valid without rendering
func (r *Renderer) ValidateTemplate(templatePath string) error {
	return r.metrics.TrackOperation("template_validation", func() error {
		_, err := pongo2.FromFile(templatePath)
		if err != nil {
			return errors.NewTemplateError("VALIDATION_FAILED", 
				fmt.Sprintf("Template validation failed: %s", templatePath), err)
		}
		return nil
	})
}

// GetTemplateVariables extracts variable names from a template
func (r *Renderer) GetTemplateVariables(templatePath string) ([]string, error) {
	result, err := r.metrics.TrackOperationWithResult("get_template_variables", func() (interface{}, error) {
		content, err := os.ReadFile(templatePath)
		if err != nil {
			return nil, errors.NewTemplateError("READ_FAILED", 
				fmt.Sprintf("Failed to read template: %s", templatePath), err)
		}

		// Simple regex to find {{ variable }} patterns
		varRegex := regexp.MustCompile(`{{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*}}`)
		matches := varRegex.FindAllSubmatch(content, -1)

		variables := make([]string, 0, len(matches))
		seen := make(map[string]bool)

		for _, match := range matches {
			varName := string(match[1])
			if !seen[varName] {
				variables = append(variables, varName)
				seen[varName] = true
			}
		}

		return variables, nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]string), nil
}

// RenderWithReader renders from an io.Reader
func (r *Renderer) RenderWithReader(reader io.Reader, vars map[string]interface{}) ([]byte, error) {
	result, err := r.metrics.TrackOperationWithResult("template_render_reader", func() (interface{}, error) {
		content, err := io.ReadAll(reader)
		if err != nil {
			return nil, errors.NewTemplateError("READ_FAILED", "Failed to read from reader", err)
		}

		return r.RenderString(string(content), vars)
	})
	if err != nil {
		return nil, err
	}
	return result.([]byte), nil
}