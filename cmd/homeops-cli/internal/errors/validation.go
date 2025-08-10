package errors

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// ValidationRule defines the interface for validation rules
type ValidationRule interface {
	Validate(value interface{}) *ValidationResult
	GetErrorCode() string
	GetDescription() string
}

// ValidationResult holds the result of a validation operation
type ValidationResult struct {
	IsValid  bool             `json:"is_valid"`
	Errors   []*HomeOpsError  `json:"errors,omitempty"`
	Warnings []*HomeOpsError  `json:"warnings,omitempty"`
	Field    string           `json:"field,omitempty"`
}

// ValidationContext provides context for validation operations
type ValidationContext struct {
	Component string                 `json:"component"`
	Operation string                 `json:"operation"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// Validator provides comprehensive validation capabilities
type Validator struct {
	rules   map[string][]ValidationRule
	context *ValidationContext
}

// NewValidator creates a new validator with the given context
func NewValidator(context *ValidationContext) *Validator {
	return &Validator{
		rules:   make(map[string][]ValidationRule),
		context: context,
	}
}

// AddRule adds a validation rule for a specific field
func (v *Validator) AddRule(field string, rule ValidationRule) {
	if v.rules[field] == nil {
		v.rules[field] = make([]ValidationRule, 0)
	}
	v.rules[field] = append(v.rules[field], rule)
}

// ValidateStruct validates a struct using reflection and registered rules
func (v *Validator) ValidateStruct(obj interface{}) *ValidationResult {
	result := &ValidationResult{
		IsValid: true,
		Errors:  make([]*HomeOpsError, 0),
		Warnings: make([]*HomeOpsError, 0),
	}
	
	val := reflect.ValueOf(obj)
	typ := reflect.TypeOf(obj)
	
	// Handle pointers
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
		typ = typ.Elem()
	}
	
	if val.Kind() != reflect.Struct {
		err := NewValidationError("INVALID_TYPE", "Expected struct type for validation", nil).
			WithContext(v.context.Operation, v.context.Component)
		result.Errors = append(result.Errors, err)
		result.IsValid = false
		return result
	}
	
	// Validate each field
	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		fieldValue := val.Field(i)
		
		// Skip unexported fields
		if !fieldValue.CanInterface() {
			continue
		}
		
		fieldName := field.Name
		
		// Check for validation tag
		if tag := field.Tag.Get("validate"); tag != "" {
			v.addTagRules(fieldName, tag)
		}
		
		// Apply rules for this field
		if rules, exists := v.rules[fieldName]; exists {
			for _, rule := range rules {
				ruleResult := rule.Validate(fieldValue.Interface())
				if ruleResult != nil {
					ruleResult.Field = fieldName
					result.Errors = append(result.Errors, ruleResult.Errors...)
					result.Warnings = append(result.Warnings, ruleResult.Warnings...)
					if !ruleResult.IsValid {
						result.IsValid = false
					}
				}
			}
		}
	}
	
	return result
}

// addTagRules parses validation tags and adds corresponding rules
func (v *Validator) addTagRules(fieldName, tag string) {
	tags := strings.Split(tag, ",")
	for _, t := range tags {
		t = strings.TrimSpace(t)
		switch {
		case t == "required":
			v.AddRule(fieldName, &RequiredRule{})
		case strings.HasPrefix(t, "min="):
			if minVal := strings.TrimPrefix(t, "min="); minVal != "" {
				if val, err := strconv.Atoi(minVal); err == nil {
					v.AddRule(fieldName, &MinLengthRule{MinLength: val})
				}
			}
		case strings.HasPrefix(t, "max="):
			if maxVal := strings.TrimPrefix(t, "max="); maxVal != "" {
				if val, err := strconv.Atoi(maxVal); err == nil {
					v.AddRule(fieldName, &MaxLengthRule{MaxLength: val})
				}
			}
		case t == "email":
			v.AddRule(fieldName, &EmailRule{})
		case t == "file_exists":
			v.AddRule(fieldName, &FileExistsRule{})
		case t == "dir_exists":
			v.AddRule(fieldName, &DirExistsRule{})
		}
	}
}



// RequiredRule validates that a field is not empty
type RequiredRule struct{}

func (r *RequiredRule) Validate(value interface{}) *ValidationResult {
	result := &ValidationResult{
		IsValid: true,
		Errors:  make([]*HomeOpsError, 0),
		Warnings: make([]*HomeOpsError, 0),
	}
	
	if value == nil {
		err := NewValidationError("REQUIRED_FIELD", "Field is required", nil)
		result.Errors = append(result.Errors, err)
		result.IsValid = false
		return result
	}
	
	// Check for empty strings
	if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
		err := NewValidationError("REQUIRED_FIELD", "Field cannot be empty", nil)
		result.Errors = append(result.Errors, err)
		result.IsValid = false
	}
	
	return result
}

func (r *RequiredRule) GetErrorCode() string {
	return "REQUIRED_FIELD"
}

func (r *RequiredRule) GetDescription() string {
	return "Field is required and cannot be empty"
}

// MinLengthRule validates minimum string length
type MinLengthRule struct {
	MinLength int
}

func (r *MinLengthRule) Validate(value interface{}) *ValidationResult {
	result := &ValidationResult{IsValid: true}
	
	if str, ok := value.(string); ok {
		if len(str) < r.MinLength {
			err := NewValidationError("MIN_LENGTH", 
				fmt.Sprintf("Field must be at least %d characters long", r.MinLength), nil)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		}
	}
	
	return result
}

func (r *MinLengthRule) GetErrorCode() string {
	return "MIN_LENGTH"
}

func (r *MinLengthRule) GetDescription() string {
	return fmt.Sprintf("Field must be at least %d characters long", r.MinLength)
}

// MaxLengthRule validates maximum string length
type MaxLengthRule struct {
	MaxLength int
}

func (r *MaxLengthRule) Validate(value interface{}) *ValidationResult {
	result := &ValidationResult{IsValid: true}
	
	if str, ok := value.(string); ok {
		if len(str) > r.MaxLength {
			err := NewValidationError("MAX_LENGTH", 
				fmt.Sprintf("Field must be no more than %d characters long", r.MaxLength), nil)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		}
	}
	
	return result
}

func (r *MaxLengthRule) GetErrorCode() string {
	return "MAX_LENGTH"
}

func (r *MaxLengthRule) GetDescription() string {
	return fmt.Sprintf("Field must be no more than %d characters long", r.MaxLength)
}

// EmailRule validates email format
type EmailRule struct{}

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

func (r *EmailRule) Validate(value interface{}) *ValidationResult {
	result := &ValidationResult{IsValid: true}
	
	if str, ok := value.(string); ok && str != "" {
		if !emailRegex.MatchString(str) {
			err := NewValidationError("INVALID_EMAIL", "Field must be a valid email address", nil)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		}
	}
	
	return result
}

func (r *EmailRule) GetErrorCode() string {
	return "INVALID_EMAIL"
}

func (r *EmailRule) GetDescription() string {
	return "Field must be a valid email address"
}

// FileExistsRule validates that a file exists
type FileExistsRule struct{}

func (r *FileExistsRule) Validate(value interface{}) *ValidationResult {
	result := &ValidationResult{IsValid: true}
	
	if str, ok := value.(string); ok && str != "" {
		// Expand environment variables and home directory
		path := os.ExpandEnv(str)
		if strings.HasPrefix(path, "~/") {
			homeDir, _ := os.UserHomeDir()
			path = filepath.Join(homeDir, path[2:])
		}
		
		if _, err := os.Stat(path); os.IsNotExist(err) {
			err := NewFileSystemError("FILE_NOT_FOUND", 
				fmt.Sprintf("File does not exist: %s", path), err)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		} else if err != nil {
			err := NewFileSystemError("FILE_ACCESS_ERROR", 
				fmt.Sprintf("Cannot access file: %s", path), err)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		}
	}
	
	return result
}

func (r *FileExistsRule) GetErrorCode() string {
	return "FILE_NOT_FOUND"
}

func (r *FileExistsRule) GetDescription() string {
	return "File must exist and be accessible"
}

// DirExistsRule validates that a directory exists
type DirExistsRule struct{}

func (r *DirExistsRule) Validate(value interface{}) *ValidationResult {
	result := &ValidationResult{IsValid: true}
	
	if str, ok := value.(string); ok && str != "" {
		// Expand environment variables and home directory
		path := os.ExpandEnv(str)
		if strings.HasPrefix(path, "~/") {
			homeDir, _ := os.UserHomeDir()
			path = filepath.Join(homeDir, path[2:])
		}
		
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			err := NewFileSystemError("DIR_NOT_FOUND", 
				fmt.Sprintf("Directory does not exist: %s", path), err)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		} else if err != nil {
			err := NewFileSystemError("DIR_ACCESS_ERROR", 
				fmt.Sprintf("Cannot access directory: %s", path), err)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		} else if !info.IsDir() {
			err := NewValidationError("NOT_DIRECTORY", 
				fmt.Sprintf("Path is not a directory: %s", path), nil)
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		}
	}
	
	return result
}

func (r *DirExistsRule) GetErrorCode() string {
	return "DIR_NOT_FOUND"
}

func (r *DirExistsRule) GetDescription() string {
	return "Directory must exist and be accessible"
}

// ConfigValidator provides validation for HomeOps configurations
type ConfigValidator struct {
	validator *Validator
}

// NewConfigValidator creates a new configuration validator
func NewConfigValidator() *ConfigValidator {
	context := &ValidationContext{
		Component: "config",
		Operation: "validation",
	}
	
	validator := NewValidator(context)
	
	// Add common configuration validation rules
	validator.AddRule("TalosVersion", &RequiredRule{})
	validator.AddRule("KubernetesVersion", &RequiredRule{})
	validator.AddRule("OnePasswordVault", &RequiredRule{})
	validator.AddRule("TalosConfig", &FileExistsRule{})
	validator.AddRule("KubeConfig", &FileExistsRule{})
	
	return &ConfigValidator{
		validator: validator,
	}
}

// ValidateBootstrapConfig validates bootstrap configuration
func (cv *ConfigValidator) ValidateBootstrapConfig(config interface{}) *ValidationResult {
	return cv.validator.ValidateStruct(config)
}

// ValidateEnvironment validates required environment variables
func (cv *ConfigValidator) ValidateEnvironment(requiredVars []string) *ValidationResult {
	result := &ValidationResult{
		IsValid: true,
		Errors:  make([]*HomeOpsError, 0),
	}
	
	for _, envVar := range requiredVars {
		if value := os.Getenv(envVar); value == "" {
			err := NewConfigError("MISSING_ENV_VAR", 
				fmt.Sprintf("Required environment variable not set: %s", envVar), nil).
				WithContext("environment_validation", "config_validator")
			result.Errors = append(result.Errors, err)
			result.IsValid = false
		}
	}
	
	return result
}

// ValidateCLITools validates that required CLI tools are available
func (cv *ConfigValidator) ValidateCLITools(requiredTools []string) *ValidationResult {
	result := &ValidationResult{
		IsValid: true,
		Errors:  make([]*HomeOpsError, 0),
	}
	
	for _, tool := range requiredTools {
		if _, err := os.Stat(tool); err != nil {
			// Try to find in PATH
			if _, pathErr := exec.LookPath(tool); pathErr != nil {
				err := NewValidationError("MISSING_CLI_TOOL", 
					fmt.Sprintf("Required CLI tool not found: %s", tool), pathErr).
					WithContext("cli_validation", "config_validator")
				result.Errors = append(result.Errors, err)
				result.IsValid = false
			}
		}
	}
	
	return result
}