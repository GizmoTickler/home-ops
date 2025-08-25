package yaml

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"dario.cat/mergo"
	"gopkg.in/yaml.v3"
	"homeops-cli/internal/errors"
	"homeops-cli/internal/metrics"
)

// Processor handles YAML operations
type Processor struct {
	logger  interface{} // Can be ColorLogger or StructuredLogger
	metrics *metrics.PerformanceCollector
}

// NewProcessor creates a new YAML processor
func NewProcessor(logger interface{}, metrics *metrics.PerformanceCollector) *Processor {
	return &Processor{
		logger:  logger,
		metrics: metrics,
	}
}

// ParseFile parses a YAML file into a map
func (p *Processor) ParseFile(filename string) (map[string]interface{}, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, errors.NewFileSystemError("YAML_FILE_READ_ERROR",
			fmt.Sprintf("failed to open YAML file: %s", filename), err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", closeErr)
		}
	}()

	return p.Parse(file)
}

// Parse parses YAML from a reader into a map
func (p *Processor) Parse(reader io.Reader) (map[string]interface{}, error) {
	var result map[string]interface{}
	decoder := yaml.NewDecoder(reader)

	if err := decoder.Decode(&result); err != nil {
		return nil, errors.NewValidationError("YAML_PARSE_ERROR",
			"failed to parse YAML content", err)
	}

	return result, nil
}

// ParseString parses YAML from a string into a map
func (p *Processor) ParseString(content string) (map[string]interface{}, error) {
	return p.Parse(strings.NewReader(content))
}

// WriteFile writes a map to a YAML file
func (p *Processor) WriteFile(filename string, data interface{}) error {
	file, err := os.Create(filename)
	if err != nil {
		return errors.NewFileSystemError("YAML_FILE_WRITE_ERROR",
			fmt.Sprintf("failed to create YAML file: %s", filename), err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", closeErr)
		}
	}()

	return p.Write(file, data)
}

// Write writes data to a writer as YAML
func (p *Processor) Write(writer io.Writer, data interface{}) error {
	encoder := yaml.NewEncoder(writer)
	encoder.SetIndent(2)
	defer func() {
		if closeErr := encoder.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close encoder: %v\n", closeErr)
		}
	}()

	if err := encoder.Encode(data); err != nil {
		return errors.NewValidationError("YAML_ENCODE_ERROR",
			"failed to encode data as YAML", err)
	}

	return nil
}

// ToString converts data to YAML string
func (p *Processor) ToString(data interface{}) (string, error) {
	var builder strings.Builder
	if err := p.Write(&builder, data); err != nil {
		return "", err
	}
	return builder.String(), nil
}

// GetValue extracts a value from YAML data using a dot-separated path
// Example: GetValue(data, "metadata.name") returns data["metadata"]["name"]
func (p *Processor) GetValue(data map[string]interface{}, path string) (interface{}, error) {
	keys := strings.Split(path, ".")
	current := data

	for i, key := range keys {
		if i == len(keys)-1 {
			// Last key, return the value
			if value, exists := current[key]; exists {
				return value, nil
			}
			return nil, errors.NewValidationError("YAML_PATH_NOT_FOUND",
				fmt.Sprintf("path '%s' not found in YAML data", path), nil)
		}

		// Navigate deeper
		if value, exists := current[key]; exists {
			if nextMap, ok := value.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return nil, errors.NewValidationError("YAML_PATH_INVALID",
					fmt.Sprintf("path '%s' contains non-object value at '%s'", path, key), nil)
			}
		} else {
			return nil, errors.NewValidationError("YAML_PATH_NOT_FOUND",
				fmt.Sprintf("path '%s' not found in YAML data", path), nil)
		}
	}

	return current, nil
}

// SetValue sets a value in YAML data using a dot-separated path
func (p *Processor) SetValue(data map[string]interface{}, path string, value interface{}) error {
	keys := strings.Split(path, ".")
	current := data

	for i, key := range keys {
		if i == len(keys)-1 {
			// Last key, set the value
			current[key] = value
			return nil
		}

		// Navigate or create deeper structure
		if value, exists := current[key]; exists {
			if nextMap, ok := value.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return errors.NewValidationError("YAML_PATH_INVALID",
					fmt.Sprintf("path '%s' contains non-object value at '%s'", path, key), nil)
			}
		} else {
			// Create new map
			newMap := make(map[string]interface{})
			current[key] = newMap
			current = newMap
		}
	}

	return nil
}

// Merge merges two YAML data structures
func (p *Processor) Merge(base, overlay map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy base
	for k, v := range base {
		result[k] = p.deepCopy(v)
	}

	// Apply overlay
	for k, v := range overlay {
		if baseValue, exists := result[k]; exists {
			if baseMap, ok := baseValue.(map[string]interface{}); ok {
				if overlayMap, ok := v.(map[string]interface{}); ok {
					// Recursively merge maps
					result[k] = p.Merge(baseMap, overlayMap)
					continue
				}
			}
		}
		// Override or set new value
		result[k] = p.deepCopy(v)
	}

	return result
}

// deepCopy creates a deep copy of a value
func (p *Processor) deepCopy(value interface{}) interface{} {
	if value == nil {
		return nil
	}

	originalValue := reflect.ValueOf(value)
	switch originalValue.Kind() {
	case reflect.Map:
		copy := make(map[string]interface{})
		for _, key := range originalValue.MapKeys() {
			copy[key.String()] = p.deepCopy(originalValue.MapIndex(key).Interface())
		}
		return copy
	case reflect.Slice:
		copy := make([]interface{}, originalValue.Len())
		for i := 0; i < originalValue.Len(); i++ {
			copy[i] = p.deepCopy(originalValue.Index(i).Interface())
		}
		return copy
	default:
		return value
	}
}

// ValidateSchema validates YAML data against a simple schema
func (p *Processor) ValidateSchema(data map[string]interface{}, requiredFields []string) error {
	for _, field := range requiredFields {
		if _, exists := data[field]; !exists {
			return errors.NewValidationError("YAML_SCHEMA_VALIDATION_ERROR",
				fmt.Sprintf("required field '%s' is missing", field), nil)
		}
	}
	return nil
}

// GetMachineType extracts machine type from YAML file
func (p *Processor) GetMachineType(filePath string) (string, error) {
	if p.metrics != nil {
		result, err := p.metrics.TrackOperationWithResult("yaml_get_machine_type", func() (interface{}, error) {
			return p.getMachineTypeInternal(filePath)
		})
		if err != nil {
			return "", err
		}
		return result.(string), nil
	}
	return p.getMachineTypeInternal(filePath)
}

func (p *Processor) getMachineTypeInternal(filePath string) (string, error) {
	data, err := p.ParseFile(filePath)
	if err != nil {
		return "", err
	}

	// Navigate to machine.type path
	if machine, ok := data["machine"]; ok {
		if machineMap, ok := machine.(map[string]interface{}); ok {
			if machineType, ok := machineMap["type"]; ok {
				if typeStr, ok := machineType.(string); ok {
					return typeStr, nil
				}
			}
		}
	}

	return "", errors.NewValidationError("YAML_FIELD_NOT_FOUND",
		"machine.type field not found in YAML", nil)
}

// MergeYAMLFiles merges two YAML files using mergo
func (p *Processor) MergeYAMLFiles(basePath, patchPath string) ([]byte, error) {
	if p.metrics != nil {
		result, err := p.metrics.TrackOperationWithResult("yaml_merge_files", func() (interface{}, error) {
			return p.mergeYAMLFilesInternal(basePath, patchPath)
		})
		if err != nil {
			return nil, err
		}
		return result.([]byte), nil
	}
	return p.mergeYAMLFilesInternal(basePath, patchPath)
}

func (p *Processor) mergeYAMLFilesInternal(basePath, patchPath string) ([]byte, error) {
	// Read base file
	baseData, err := p.ParseFile(basePath)
	if err != nil {
		return nil, errors.NewFileSystemError("YAML_READ_BASE_FAILED",
			fmt.Sprintf("Failed to read base file: %s", basePath), err)
	}

	// Read patch file
	patchData, err := p.ParseFile(patchPath)
	if err != nil {
		return nil, errors.NewFileSystemError("YAML_READ_PATCH_FAILED",
			fmt.Sprintf("Failed to read patch file: %s", patchPath), err)
	}

	// Merge patch into base using mergo
	if err := mergo.Merge(&baseData, patchData, mergo.WithOverride); err != nil {
		return nil, errors.NewValidationError("YAML_MERGE_FAILED",
			"Failed to merge YAML content", err)
	}

	// Marshal back to YAML
	merged, err := yaml.Marshal(baseData)
	if err != nil {
		return nil, errors.NewValidationError("YAML_MARSHAL_FAILED",
			"Failed to marshal merged YAML", err)
	}

	return merged, nil
}

// MergeYAML merges two YAML byte arrays
func (p *Processor) MergeYAML(baseContent, patchContent []byte) ([]byte, error) {
	if p.metrics != nil {
		result, err := p.metrics.TrackOperationWithResult("yaml_merge_content", func() (interface{}, error) {
			return p.mergeYAMLInternal(baseContent, patchContent)
		})
		if err != nil {
			return nil, err
		}
		return result.([]byte), nil
	}
	return p.mergeYAMLInternal(baseContent, patchContent)
}

// MergeYAMLMultiDocument merges YAML content while preserving additional documents in patch
func (p *Processor) MergeYAMLMultiDocument(baseContent, patchContent []byte) ([]byte, error) {
	if p.metrics != nil {
		result, err := p.metrics.TrackOperationWithResult("yaml_merge_multidoc", func() (interface{}, error) {
			return p.mergeYAMLMultiDocumentInternal(baseContent, patchContent)
		})
		if err != nil {
			return nil, err
		}
		return result.([]byte), nil
	}
	return p.mergeYAMLMultiDocumentInternal(baseContent, patchContent)
}

func (p *Processor) mergeYAMLMultiDocumentInternal(baseContent, patchContent []byte) ([]byte, error) {
	// Split patch content by document separator
	patchStr := string(patchContent)
	parts := strings.Split(patchStr, "---")

	if len(parts) == 1 {
		// Single document, use regular merge
		return p.mergeYAMLInternal(baseContent, patchContent)
	}

	// Multi-document patch: merge first document, append the rest
	firstDoc := strings.TrimSpace(parts[0])
	if firstDoc == "" && len(parts) > 1 {
		firstDoc = strings.TrimSpace(parts[1])
		parts = parts[2:]
	} else {
		parts = parts[1:]
	}

	// Merge the first document with base
	merged, err := p.mergeYAMLInternal(baseContent, []byte(firstDoc))
	if err != nil {
		return nil, err
	}

	// Append remaining documents
	result := strings.Builder{}
	result.Write(merged)

	for _, part := range parts {
		trimmedPart := strings.TrimSpace(part)
		if trimmedPart != "" {
			result.WriteString("\n---\n")
			result.WriteString(trimmedPart)
		}
	}

	return []byte(result.String()), nil
}

func (p *Processor) mergeYAMLInternal(baseContent, patchContent []byte) ([]byte, error) {
	// Parse base YAML
	var baseData map[string]interface{}
	if err := yaml.Unmarshal(baseContent, &baseData); err != nil {
		return nil, errors.NewValidationError("YAML_PARSE_BASE_FAILED",
			"Failed to parse base YAML content", err)
	}

	// Parse patch YAML
	var patchData map[string]interface{}
	if err := yaml.Unmarshal(patchContent, &patchData); err != nil {
		return nil, errors.NewValidationError("YAML_PARSE_PATCH_FAILED",
			"Failed to parse patch YAML content", err)
	}

	// Merge patch into base using mergo
	if err := mergo.Merge(&baseData, patchData, mergo.WithOverride); err != nil {
		return nil, errors.NewValidationError("YAML_MERGE_FAILED",
			"Failed to merge YAML content", err)
	}

	// Marshal back to YAML
	merged, err := yaml.Marshal(baseData)
	if err != nil {
		return nil, errors.NewValidationError("YAML_MARSHAL_FAILED",
			"Failed to marshal merged YAML", err)
	}

	return merged, nil
}
