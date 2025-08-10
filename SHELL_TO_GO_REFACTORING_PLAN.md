# Shell Script to Go Refactoring Plan

This document outlines the comprehensive plan to eliminate shell script dependencies and move to pure Go implementations in the homeops-cli project.

## Current Shell Dependencies to Refactor

### 1. Template Rendering Pipeline

**Current Implementation:**
- `minijinja-cli` for Jinja2 template rendering
- `op inject` for 1Password secret injection
- Located in: `renderTemplate()` functions in `bootstrap.go` and `talos.go`

**Refactoring Target:**
```go
// Replace renderTemplate() function in bootstrap.go and talos.go
func renderTemplate(file string) ([]byte, error) {
    // Current: exec.Command("minijinja-cli", file) | exec.Command("op", "inject")
    // New: Use github.com/flosch/pongo2 or text/template + 1Password Go SDK
}
```

**Required Go Libraries:**
- `github.com/flosch/pongo2/v6` - Django-style templates (closest to Jinja2)
- `github.com/1Password/connect-sdk-go` - Official 1Password SDK
- `text/template` or `html/template` - Built-in Go templating (alternative)

### 2. YAML Processing

**Current Implementation:**
- `yq` commands for YAML manipulation
- Located in: `getMachineType()` function

**Refactoring Target:**
```go
// Replace getMachineType() function
func getMachineType(nodeFile string) (string, error) {
    // Current: exec.Command("yq", ".machine.type", nodeFile)
    // New: Use yaml.v3 to parse and extract values
    data, err := os.ReadFile(nodeFile)
    if err != nil {
        return "", err
    }
    
    var config map[string]interface{}
    if err := yaml.Unmarshal(data, &config); err != nil {
        return "", err
    }
    
    return config["machine"].(map[string]interface{})["type"].(string), nil
}
```

**Required Go Libraries:**
- `gopkg.in/yaml.v3` (already imported)
- `github.com/mikefarah/yq/v4` - Go library version of yq (alternative)

### 3. Talos Configuration Patching

**Current Implementation:**
- `talosctl machineconfig patch` for YAML merging
- Located in: `applyTalosPatch()` function

**Refactoring Target:**
```go
// Replace applyTalosPatch() function
func applyTalosPatch(base, patch []byte) ([]byte, error) {
    // Current: talosctl machineconfig patch
    // New: Use github.com/imdario/mergo or custom YAML merge logic
    var baseConfig, patchConfig map[string]interface{}
    
    yaml.Unmarshal(base, &baseConfig)
    yaml.Unmarshal(patch, &patchConfig)
    
    mergo.Merge(&baseConfig, patchConfig, mergo.WithOverride)
    
    return yaml.Marshal(baseConfig)
}
```

**Required Go Libraries:**
- `github.com/imdario/mergo` - Deep merging of structs/maps
- `github.com/mikefarah/yq/v4` - Go library version of yq (alternative)

### 4. Kubernetes Operations

**Current Implementation:**
- `kubectl`, `kustomize`, `helmfile` commands
- Located in: `kubernetes.go`, various functions

**Refactoring Target:**
```go
// Replace kubectl exec calls in kubernetes.go
func syncSecrets(dryRun bool) error {
    // Current: exec.Command("kubectl", "annotate", "externalsecrets"...)
    // New: Use k8s.io/client-go
    config, err := rest.InClusterConfig()
    if err != nil {
        config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
    }
    
    clientset, err := kubernetes.NewForConfig(config)
    if err != nil {
        return err
    }
    
    // Use clientset to perform operations
}
```

**Required Go Libraries:**
- `k8s.io/client-go` - Official Kubernetes Go client
- `sigs.k8s.io/controller-runtime/pkg/client` - Higher-level K8s client
- `helm.sh/helm/v3/pkg/action` - Helm Go SDK

### 5. Shell Script Files to Eliminate

**Files to Remove:**
- `scripts/render-machine-config.sh` - Replace with Go template renderer
- `scripts/lib/common.sh` - Replace with Go utility functions
- `scripts/bootstrap-cluster.sh` - Already replaced by `homeops bootstrap`

## Implementation Strategy

### Phase 1: Template Engine Refactoring (High Priority)

**Step 1: Add Dependencies**
```bash
cd cmd/homeops-cli
go get github.com/flosch/pongo2/v6
go get github.com/1Password/connect-sdk-go
go get github.com/imdario/mergo
```

**Step 2: Create Template Renderer**
```go
// internal/template/renderer.go
package template

import (
    "github.com/flosch/pongo2/v6"
    "github.com/1Password/connect-sdk-go"
)

type Renderer struct {
    opClient *onepassword.Client
}

func NewRenderer(opToken string) (*Renderer, error) {
    client := onepassword.NewClient(opToken)
    return &Renderer{opClient: client}, nil
}

func (r *Renderer) RenderWithSecrets(templatePath string, vars map[string]interface{}) ([]byte, error) {
    // 1. Load and parse Pongo2 template
    tpl, err := pongo2.FromFile(templatePath)
    if err != nil {
        return nil, err
    }
    
    // 2. Render template with environment variables
    rendered, err := tpl.Execute(vars)
    if err != nil {
        return nil, err
    }
    
    // 3. Inject 1Password secrets
    return r.injectSecrets([]byte(rendered))
}

func (r *Renderer) injectSecrets(content []byte) ([]byte, error) {
    // Parse op:// references and replace with actual secrets
    // Implementation details...
}
```

**Step 3: Update Bootstrap and Talos Commands**
- Replace `renderTemplate()` calls with new Go implementation
- Update error handling and logging

### Phase 2: YAML Processing (Medium Priority)

**Step 1: Create YAML Processor**
```go
// internal/yaml/processor.go
package yaml

import (
    "gopkg.in/yaml.v3"
    "os"
)

type Processor struct{}

func NewProcessor() *Processor {
    return &Processor{}
}

func (p *Processor) GetMachineType(nodeFile string) (string, error) {
    data, err := os.ReadFile(nodeFile)
    if err != nil {
        return "", err
    }
    
    var config map[string]interface{}
    if err := yaml.Unmarshal(data, &config); err != nil {
        return "", err
    }
    
    machine, ok := config["machine"].(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("invalid machine config")
    }
    
    machineType, ok := machine["type"].(string)
    if !ok {
        return "", fmt.Errorf("machine type not found")
    }
    
    return machineType, nil
}

func (p *Processor) MergeConfigs(base, patch []byte) ([]byte, error) {
    var baseConfig, patchConfig map[string]interface{}
    
    if err := yaml.Unmarshal(base, &baseConfig); err != nil {
        return nil, err
    }
    
    if err := yaml.Unmarshal(patch, &patchConfig); err != nil {
        return nil, err
    }
    
    if err := mergo.Merge(&baseConfig, patchConfig, mergo.WithOverride); err != nil {
        return nil, err
    }
    
    return yaml.Marshal(baseConfig)
}
```

### Phase 3: Kubernetes Client Integration (Medium Priority)

**Step 1: Add K8s Dependencies**
```bash
go get k8s.io/client-go@latest
go get k8s.io/apimachinery@latest
go get helm.sh/helm/v3@latest
```

**Step 2: Create K8s Client Wrapper**
```go
// internal/k8s/client.go
package k8s

import (
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/clientcmd"
)

type Client struct {
    clientset kubernetes.Interface
    config    *rest.Config
}

func NewClient(kubeconfigPath string) (*Client, error) {
    config, err := rest.InClusterConfig()
    if err != nil {
        config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
        if err != nil {
            return nil, err
        }
    }
    
    clientset, err := kubernetes.NewForConfig(config)
    if err != nil {
        return nil, err
    }
    
    return &Client{
        clientset: clientset,
        config:    config,
    }, nil
}

func (c *Client) AnnotateExternalSecrets() error {
    // Replace kubectl annotate commands with direct API calls
    // Implementation details...
}

func (c *Client) WaitForNodes() error {
    // Replace kubectl wait commands with watch API
    // Implementation details...
}
```

### Phase 4: Configuration Merging (Low Priority)

**Step 1: Implement Advanced YAML Merging**
```go
// internal/talos/config.go
package talos

import (
    "github.com/imdario/mergo"
    "gopkg.in/yaml.v3"
)

type ConfigManager struct{}

func NewConfigManager() *ConfigManager {
    return &ConfigManager{}
}

func (cm *ConfigManager) MergeConfigs(base, patch []byte) ([]byte, error) {
    var baseConfig, patchConfig map[string]interface{}
    
    if err := yaml.Unmarshal(base, &baseConfig); err != nil {
        return nil, fmt.Errorf("failed to unmarshal base config: %w", err)
    }
    
    if err := yaml.Unmarshal(patch, &patchConfig); err != nil {
        return nil, fmt.Errorf("failed to unmarshal patch config: %w", err)
    }
    
    // Use mergo for deep merging with override
    if err := mergo.Merge(&baseConfig, patchConfig, mergo.WithOverride); err != nil {
        return nil, fmt.Errorf("failed to merge configs: %w", err)
    }
    
    result, err := yaml.Marshal(baseConfig)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal merged config: %w", err)
    }
    
    return result, nil
}
```

## Benefits of Full Go Refactoring

### Performance Improvements
- **Eliminate Process Spawning:** No more `exec.Command()` overhead
- **Memory Efficiency:** Direct memory operations instead of temporary files
- **Faster Startup:** No external tool initialization

### Development Benefits
- **Better Error Handling:** Structured error propagation and handling
- **Unit Testing:** All functions become unit testable without external dependencies
- **Type Safety:** Compile-time validation of configurations
- **IDE Support:** Better code completion and refactoring support

### Deployment Benefits
- **Single Binary:** No external tool requirements
- **Cross-Platform:** No shell script compatibility issues
- **Simplified Installation:** Just download and run the binary
- **Reduced Attack Surface:** Fewer external dependencies

### Maintenance Benefits
- **Consistent Error Messages:** Unified error handling across all operations
- **Better Logging:** Structured logging with consistent format
- **Easier Debugging:** Step-through debugging in Go instead of shell scripts
- **Version Control:** All logic in version-controlled Go code

## Migration Timeline

### Phase 1: Foundation and YAML Processing (Weeks 1-3)
- Week 1: 
  - [ ] Set up configuration management with Viper
  - [ ] Implement structured error handling
  - [ ] Enhanced logging with Zap
- Week 2: 
  - [ ] Replace yq with gopkg.in/yaml.v3
  - [ ] Implement structured YAML operations
  - [ ] Add performance monitoring framework
- Week 3: 
  - [ ] Security enhancements for secret handling
  - [ ] Comprehensive testing setup
  - [ ] Migration tools foundation

### Phase 2: Template Engine and Secret Management (Weeks 4-6)
- Week 4: 
  - [ ] Pongo2 integration and basic template rendering
  - [ ] Secure secret manager implementation
- Week 5: 
  - [ ] 1Password SDK integration with caching
  - [ ] Template rendering with secret injection
- Week 6: 
  - [ ] Testing and validation against existing templates
  - [ ] Performance benchmarking

### Phase 3: Kubernetes Client Integration (Weeks 7-9)
- Week 7: 
  - [ ] Basic kubectl operations replacement (prioritize syncSecrets)
  - [ ] Kubernetes client setup with proper error handling
- Week 8: 
  - [ ] Helm client integration
  - [ ] Advanced Kubernetes operations
- Week 9: 
  - [ ] Integration testing with real clusters
  - [ ] Performance optimization

### Phase 4: Configuration Merging and Migration Tools (Weeks 10-12)
- Week 10: 
  - [ ] Talos configuration patching with mergo
  - [ ] Migration comparison tools
- Week 11: 
  - [ ] Final integration and comprehensive testing
  - [ ] Migration validation tools
- Week 12: 
  - [ ] Documentation and rollback procedures
  - [ ] Production readiness validation

## Missing Considerations and Additional Requirements

### 1. Configuration Management
**Current Gap:** No centralized configuration management strategy

**Required Implementation:**
```go
// Add viper for configuration management
go get github.com/spf13/viper

// internal/config/manager.go
type Config struct {
    TalosVersion     string `mapstructure:"talos_version"`
    KubernetesVersion string `mapstructure:"kubernetes_version"`
    OnePasswordVault  string `mapstructure:"onepassword_vault"`
    TrueNASHost      string `mapstructure:"truenas_host"`
    LogLevel         string `mapstructure:"log_level"`
}

func LoadConfig() (*Config, error) {
    viper.SetConfigName("homeops")
    viper.SetConfigType("yaml")
    viper.AddConfigPath(".")
    viper.AddConfigPath("$HOME/.config/homeops")
    
    // Environment variable support
    viper.SetEnvPrefix("HOMEOPS")
    viper.AutomaticEnv()
    
    var config Config
    if err := viper.ReadInConfig(); err != nil {
        return nil, err
    }
    
    return &config, viper.Unmarshal(&config)
}
```

### 2. Enhanced Error Handling
**Current Gap:** Basic error handling without structured error types

**Required Implementation:**
```go
// internal/errors/types.go
type ErrorType string

const (
    ErrTypeTemplate    ErrorType = "template"
    ErrTypeKubernetes  ErrorType = "kubernetes"
    ErrTypeTalos       ErrorType = "talos"
    ErrTypeValidation  ErrorType = "validation"
    ErrTypeNetwork     ErrorType = "network"
)

type HomeOpsError struct {
    Type    ErrorType `json:"type"`
    Code    string    `json:"code"`
    Message string    `json:"message"`
    Details map[string]interface{} `json:"details,omitempty"`
    Cause   error     `json:"-"`
}

func (e *HomeOpsError) Error() string {
    if e.Cause != nil {
        return fmt.Sprintf("%s: %s (caused by: %v)", e.Code, e.Message, e.Cause)
    }
    return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func NewTemplateError(code, message string, cause error) *HomeOpsError {
    return &HomeOpsError{
        Type:    ErrTypeTemplate,
        Code:    code,
        Message: message,
        Cause:   cause,
    }
}
```

### 3. Structured Logging Enhancement
**Current Gap:** Basic color logging without structured fields

**Required Implementation:**
```go
// Enhanced logger with structured logging
go get go.uber.org/zap
go get go.uber.org/zap/zapcore

// internal/common/logger.go enhancement
type StructuredLogger struct {
    logger *zap.Logger
    sugar  *zap.SugaredLogger
}

func NewStructuredLogger(level string) (*StructuredLogger, error) {
    config := zap.NewProductionConfig()
    config.Level = zap.NewAtomicLevelAt(parseLogLevel(level))
    config.EncoderConfig.TimeKey = "timestamp"
    config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
    
    logger, err := config.Build()
    if err != nil {
        return nil, err
    }
    
    return &StructuredLogger{
        logger: logger,
        sugar:  logger.Sugar(),
    }, nil
}

func (l *StructuredLogger) WithFields(fields map[string]interface{}) *StructuredLogger {
    var zapFields []zap.Field
    for k, v := range fields {
        zapFields = append(zapFields, zap.Any(k, v))
    }
    return &StructuredLogger{
        logger: l.logger.With(zapFields...),
        sugar:  l.logger.With(zapFields...).Sugar(),
    }
}
```

### 4. Comprehensive Testing Strategy
**Current Gap:** No specific testing approach for shell-to-Go migration

**Required Implementation:**
```go
// Add testing dependencies
go get github.com/stretchr/testify
go get github.com/golang/mock/gomock
go get github.com/onsi/ginkgo/v2
go get github.com/onsi/gomega

// Testing strategy:
// 1. Unit tests for each refactored component
// 2. Integration tests comparing shell vs Go outputs
// 3. End-to-end tests for complete workflows
// 4. Performance benchmarks

// Example: cmd/homeops-cli/internal/template/renderer_test.go
func TestTemplateRenderer_CompareWithShell(t *testing.T) {
    tests := []struct {
        name         string
        templateFile string
        vars         map[string]interface{}
    }{
        {
            name:         "controlplane_config",
            templateFile: "../../talos/controlplane.yaml.j2",
            vars:         map[string]interface{}{"node_ip": "192.168.1.10"},
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test Go implementation
            renderer := NewRenderer("")
            goResult, err := renderer.RenderWithSecrets(tt.templateFile, tt.vars)
            require.NoError(t, err)
            
            // Test shell implementation (for comparison)
            shellResult := executeShellTemplate(tt.templateFile)
            
            // Compare outputs (normalize whitespace/formatting)
            assert.Equal(t, normalizeYAML(shellResult), normalizeYAML(goResult))
        })
    }
}
```

### 5. Security Enhancements
**Current Gap:** No explicit security considerations for secret handling

**Required Implementation:**
```go
// internal/security/secrets.go
type SecretManager struct {
    cache      map[string]CachedSecret
    cacheTTL   time.Duration
    encryption *encryption.Manager
}

type CachedSecret struct {
    Value     []byte    // Encrypted in memory
    ExpiresAt time.Time
}

// Secure secret caching with encryption
func (sm *SecretManager) GetSecret(reference string) (string, error) {
    // Check cache first
    if cached, exists := sm.cache[reference]; exists {
        if time.Now().Before(cached.ExpiresAt) {
            decrypted, err := sm.encryption.Decrypt(cached.Value)
            return string(decrypted), err
        }
        delete(sm.cache, reference) // Expired
    }
    
    // Fetch from 1Password
    secret, err := sm.fetchFromOnePassword(reference)
    if err != nil {
        return "", err
    }
    
    // Cache encrypted version
    encrypted, err := sm.encryption.Encrypt([]byte(secret))
    if err == nil {
        sm.cache[reference] = CachedSecret{
            Value:     encrypted,
            ExpiresAt: time.Now().Add(sm.cacheTTL),
        }
    }
    
    return secret, nil
}

// Clear sensitive data from memory
func (sm *SecretManager) ClearCache() {
    for k := range sm.cache {
        delete(sm.cache, k)
    }
}
```

### 6. Performance Monitoring and Metrics
**Current Gap:** No performance tracking during migration

**Required Implementation:**
```go
// internal/metrics/collector.go
type PerformanceCollector struct {
    operations map[string]*OperationMetrics
    mu         sync.RWMutex
}

type OperationMetrics struct {
    TotalCalls    int64
    TotalDuration time.Duration
    Errors        int64
    LastExecution time.Time
}

func (pc *PerformanceCollector) TrackOperation(name string, fn func() error) error {
    start := time.Now()
    err := fn()
    duration := time.Since(start)
    
    pc.mu.Lock()
    defer pc.mu.Unlock()
    
    if pc.operations[name] == nil {
        pc.operations[name] = &OperationMetrics{}
    }
    
    metrics := pc.operations[name]
    metrics.TotalCalls++
    metrics.TotalDuration += duration
    metrics.LastExecution = start
    
    if err != nil {
        metrics.Errors++
    }
    
    return err
}

func (pc *PerformanceCollector) GetReport() map[string]OperationReport {
    pc.mu.RLock()
    defer pc.mu.RUnlock()
    
    report := make(map[string]OperationReport)
    for name, metrics := range pc.operations {
        avgDuration := time.Duration(0)
        if metrics.TotalCalls > 0 {
            avgDuration = metrics.TotalDuration / time.Duration(metrics.TotalCalls)
        }
        
        report[name] = OperationReport{
            AverageDuration: avgDuration,
            TotalCalls:      metrics.TotalCalls,
            ErrorRate:       float64(metrics.Errors) / float64(metrics.TotalCalls),
            LastExecution:   metrics.LastExecution,
        }
    }
    
    return report
}
```

### 7. Backward Compatibility and Migration Tools
**Current Gap:** No migration assistance for users

**Required Implementation:**
```go
// cmd/homeops-cli/cmd/migrate/migrate.go
func newMigrateCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "migrate",
        Short: "Migration utilities for shell-to-Go transition",
        Long:  "Tools to help migrate from shell-based workflows to Go implementations",
    }
    
    cmd.AddCommand(
        newValidateConfigCommand(),
        newCompareOutputCommand(),
        newBenchmarkCommand(),
    )
    
    return cmd
}

func newCompareOutputCommand() *cobra.Command {
    return &cobra.Command{
        Use:   "compare",
        Short: "Compare shell vs Go implementation outputs",
        RunE: func(cmd *cobra.Command, args []string) error {
            return compareImplementations()
        },
    }
}

func compareImplementations() error {
    // Compare template rendering
    templateDiffs := compareTemplateOutputs()
    
    // Compare YAML processing
    yamlDiffs := compareYAMLProcessing()
    
    // Compare Kubernetes operations
    k8sDiffs := compareKubernetesOperations()
    
    // Generate comparison report
    return generateComparisonReport(templateDiffs, yamlDiffs, k8sDiffs)
}
```

## Risk Mitigation

### Compatibility Risks
- **Template Compatibility:** Ensure Pongo2 templates work with existing Jinja2 templates
- **YAML Merging:** Verify mergo produces identical results to talosctl patch
- **Secret Injection:** Ensure 1Password SDK handles all op:// reference formats
- **Configuration Drift:** Validate that Go implementations produce identical outputs to shell versions
- **Performance Regression:** Ensure Go implementations meet or exceed shell script performance

### Security Risks
- **Secret Exposure:** Implement secure memory handling for sensitive data
- **Cache Poisoning:** Validate and encrypt cached secrets
- **Dependency Vulnerabilities:** Regular security scanning of Go dependencies

### Mitigation Strategies
- **Parallel Implementation:** Keep shell scripts until Go implementation is fully tested
- **Feature Flags:** Allow switching between shell and Go implementations
- **Comprehensive Testing:** Test all edge cases and error conditions
- **Gradual Migration:** Migrate one component at a time
- **Security Audits:** Regular security reviews of secret handling code
- **Performance Monitoring:** Continuous performance tracking during migration
- **Rollback Plan:** Ability to quickly revert to shell implementations if issues arise

## Success Criteria

### Functional Requirements
- [ ] All template rendering produces identical output to minijinja-cli + op inject
- [ ] All YAML operations produce identical results to yq
- [ ] All Talos operations work identically to talosctl
- [ ] All Kubernetes operations work identically to kubectl
- [ ] Secret injection works seamlessly with 1Password
- [ ] Configuration management works across all environments
- [ ] Migration tools successfully validate shell vs Go outputs
- [ ] Structured error handling provides actionable error messages

### Performance Requirements
- [ ] Template rendering: <2s for typical configurations (target: 50% faster than shell)
- [ ] YAML processing: <500ms for typical files (target: 70% faster than yq)
- [ ] Kubernetes operations: <5s for typical operations (target: 30% faster than kubectl)
- [ ] Overall CLI startup: <100ms (target: 80% faster than shell scripts)
- [ ] Secret caching reduces 1Password API calls by 80%
- [ ] Memory usage remains under 50MB for typical operations

### Quality Requirements
- [ ] 90%+ test coverage for new Go code
- [ ] Zero regression in existing functionality
- [ ] Comprehensive error handling and logging with structured output
- [ ] Cross-platform compatibility (Linux, macOS, Windows)
- [ ] Security audit passes for secret handling
- [ ] Performance benchmarks show improvement over shell implementations
- [ ] Migration validation tools report 100% output compatibility

### Security Requirements
- [ ] Secrets are encrypted in memory cache
- [ ] No secrets logged or exposed in error messages
- [ ] Secure cleanup of sensitive data
- [ ] Dependency vulnerability scanning passes
- [ ] Secret cache TTL properly enforced

### Operational Requirements
- [ ] Rollback capability to shell implementations
- [ ] Performance monitoring and alerting
- [ ] Configuration validation and migration assistance
- [ ] Comprehensive documentation for new features
- [ ] Backward compatibility maintained during transition

## Enhanced Dependencies

The refactoring will add the following Go dependencies to improve functionality:

### Core Dependencies
```go
// Configuration Management
github.com/spf13/viper v1.17.0

// Enhanced Logging
go.uber.org/zap v1.26.0
go.uber.org/zap/zapcore v1.26.0

// Testing Framework
github.com/stretchr/testify v1.8.4
github.com/golang/mock/gomock v1.6.0
github.com/onsi/ginkgo/v2 v2.13.0
github.com/onsi/gomega v1.29.0

// Security and Encryption
crypto/aes // Standard library
crypto/cipher // Standard library
crypto/rand // Standard library

// Performance Monitoring
sync // Standard library
time // Standard library
```

### Existing Dependencies (Enhanced Usage)
```go
// Template Engine (already planned)
github.com/flosch/pongo2/v6 v6.0.0

// 1Password Integration (already planned)
github.com/1Password/connect-sdk-go v1.5.3

// YAML Processing (already planned)
gopkg.in/yaml.v3 v3.0.1
github.com/imdario/mergo v0.3.16

// Kubernetes Client (already planned)
k8s.io/client-go v0.28.4
helm.sh/helm/v3/pkg/action v3.13.2
```

## Post-Migration Cleanup

### Remove Shell Dependencies
- [ ] Remove minijinja-cli installation from bootstrap scripts
- [ ] Remove yq installation requirements
- [ ] Update documentation to reflect Go-only dependencies
- [ ] Clean up any remaining shell script files
- [ ] Remove shell-based error handling and logging
- [ ] Archive migration comparison tools after validation

### Update CI/CD
- [ ] Remove shell tool installations from CI pipelines
- [ ] Update container images to remove unnecessary tools
- [ ] Simplify deployment processes
- [ ] Add Go dependency vulnerability scanning
- [ ] Update performance benchmarking in CI
- [ ] Add security scanning for secret handling

### Files to Remove
- `scripts/render-machine-config.sh`
- `scripts/lib/common.sh`
- `scripts/bootstrap-cluster.sh` (if not already removed)
- Any remaining shell script utilities

### Documentation Updates
- Update README.md to reflect Go-only implementation
- Update installation instructions
- Update development setup guide
- Create migration guide for users
- Document new configuration management
- Add security best practices documentation
- Create performance tuning guide
- Document rollback procedures

### Security Cleanup
- [ ] Audit and remove any hardcoded secrets
- [ ] Validate secure memory handling implementation
- [ ] Review and update secret caching policies
- [ ] Conduct final security audit
- [ ] Update security documentation

This refactoring will transform homeops-cli into a truly self-contained Go application, eliminating external dependencies and improving maintainability, performance, and developer experience.