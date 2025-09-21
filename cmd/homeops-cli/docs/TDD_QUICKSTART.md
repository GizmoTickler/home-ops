# Test-Driven Development (TDD) Quick Start Guide

This guide helps you get started with Test-Driven Development for the HomeOps CLI project.

## Quick Setup

### 1. Install Required Tools

```bash
# Install test dependencies
go install github.com/stretchr/testify@latest
go install golang.org/x/perf/cmd/benchstat@latest

# Install watch tool for TDD workflow (macOS)
brew install entr

# Or for Linux
sudo apt-get install entr
```

### 2. Verify Test Environment

```bash
cd cmd/homeops-cli
make test-unit
```

## TDD Workflow

### Red-Green-Refactor Cycle

#### 1. 🔴 RED: Write a Failing Test

Create a test that describes the desired behavior:

```go
// cmd/talos/new_feature_test.go
func TestNewVMValidation(t *testing.T) {
    tests := []struct {
        name    string
        vmSpec  VMSpec
        wantErr bool
    }{
        {
            name: "valid VM spec",
            vmSpec: VMSpec{
                Name:   "test_vm",
                CPUs:   4,
                Memory: 8192,
            },
            wantErr: false,
        },
        {
            name: "invalid VM name",
            vmSpec: VMSpec{
                Name:   "test-vm", // dashes not allowed
                CPUs:   4,
                Memory: 8192,
            },
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validateVMSpec(tt.vmSpec)
            if tt.wantErr {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

#### 2. 🟢 GREEN: Make the Test Pass

Implement minimal code to make the test pass:

```go
// cmd/talos/vm.go
type VMSpec struct {
    Name   string
    CPUs   int
    Memory int
}

func validateVMSpec(spec VMSpec) error {
    if strings.Contains(spec.Name, "-") {
        return fmt.Errorf("VM name cannot contain dashes")
    }
    return nil
}
```

#### 3. 🔵 REFACTOR: Improve the Code

Refactor while keeping tests green:

```go
func validateVMSpec(spec VMSpec) error {
    if err := validateVMName(spec.Name); err != nil {
        return fmt.Errorf("invalid VM name: %w", err)
    }

    if err := validateVMResources(spec.CPUs, spec.Memory); err != nil {
        return fmt.Errorf("invalid VM resources: %w", err)
    }

    return nil
}

func validateVMName(name string) error {
    if name == "" {
        return fmt.Errorf("name cannot be empty")
    }
    if strings.Contains(name, "-") {
        return fmt.Errorf("name cannot contain dashes")
    }
    if strings.Contains(name, " ") {
        return fmt.Errorf("name cannot contain spaces")
    }
    return nil
}

func validateVMResources(cpus int, memory int) error {
    if cpus <= 0 {
        return fmt.Errorf("CPUs must be positive")
    }
    if memory <= 0 {
        return fmt.Errorf("memory must be positive")
    }
    return nil
}
```

## TDD Commands

### Start TDD Mode

```bash
# Start TDD mode - watches for changes and runs tests
make test-tdd
```

This command:
- Watches all `.go` files for changes
- Automatically runs tests when files change
- Clears screen between runs
- Shows only short tests (unit tests)
- Uses `-failfast` flag to stop on first failure

### Alternative TDD Commands

```bash
# Watch mode without clearing screen
make test-watch

# Run specific test repeatedly
make test-specific TEST=TestNewVMValidation

# Test specific package in TDD mode
find cmd/talos/*.go | entr -c go test -v -short -failfast ./cmd/talos
```

## TDD Example: Adding a New Command

Let's walk through adding a new `validate-config` command using TDD.

### Step 1: Write the Test First

```go
// cmd/talos/validate_test.go
package talos

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestNewValidateConfigCommand(t *testing.T) {
    cmd := newValidateConfigCommand()

    assert.NotNil(t, cmd)
    assert.Equal(t, "validate-config", cmd.Use)
    assert.NotEmpty(t, cmd.Short)
    assert.NotEmpty(t, cmd.Long)
}

func TestValidateConfigWithValidFile(t *testing.T) {
    tmpDir, cleanup := testutil.TempDir(t)
    defer cleanup()

    // Create valid config file
    configPath := testutil.TempFile(t, tmpDir, "config-*.yaml", []byte(`
machine:
  type: controlplane
cluster:
  name: test-cluster
`))

    err := validateTalosConfig(configPath)
    assert.NoError(t, err)
}

func TestValidateConfigWithInvalidFile(t *testing.T) {
    tmpDir, cleanup := testutil.TempDir(t)
    defer cleanup()

    // Create invalid config file
    configPath := testutil.TempFile(t, tmpDir, "config-*.yaml", []byte(`
invalid: yaml: content
  - malformed
`))

    err := validateTalosConfig(configPath)
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "invalid YAML")
}
```

### Step 2: Run Tests (Should Fail)

```bash
make test-specific TEST=TestNewValidateConfigCommand
# Expected: FAIL - function doesn't exist
```

### Step 3: Create Minimal Implementation

```go
// cmd/talos/validate.go
package talos

import (
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/cobra"
    "gopkg.in/yaml.v3"
)

func newValidateConfigCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "validate-config",
        Short: "Validate Talos configuration files",
        Long:  "Validate the syntax and structure of Talos configuration files",
        RunE: func(cmd *cobra.Command, args []string) error {
            if len(args) == 0 {
                return fmt.Errorf("config file path required")
            }
            return validateTalosConfig(args[0])
        },
    }

    return cmd
}

func validateTalosConfig(configPath string) error {
    // Check if file exists
    if _, err := os.Stat(configPath); os.IsNotExist(err) {
        return fmt.Errorf("config file not found: %s", configPath)
    }

    // Read file
    data, err := os.ReadFile(configPath)
    if err != nil {
        return fmt.Errorf("failed to read config file: %w", err)
    }

    // Validate YAML syntax
    var config map[string]interface{}
    if err := yaml.Unmarshal(data, &config); err != nil {
        return fmt.Errorf("invalid YAML: %w", err)
    }

    return nil
}
```

### Step 4: Add Command to Parent

```go
// cmd/talos/talos.go - in NewCommand() function
func NewCommand() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "talos",
        Short: "Talos Linux cluster management",
        Long:  "Commands for managing Talos Linux cluster nodes and VMs",
    }

    // Add existing commands...
    cmd.AddCommand(newApplyNodeCommand())
    cmd.AddCommand(newUpgradeNodeCommand())
    // ... other commands ...

    // Add new command
    cmd.AddCommand(newValidateConfigCommand())

    return cmd
}
```

### Step 5: Run Tests (Should Pass)

```bash
make test-specific TEST=TestNewValidateConfigCommand
# Expected: PASS
```

### Step 6: Refactor and Add More Tests

Add more comprehensive validation:

```go
// Add more test cases
func TestValidateConfigRequiredFields(t *testing.T) {
    tests := []struct {
        name    string
        config  string
        wantErr bool
        errMsg  string
    }{
        {
            name: "missing machine type",
            config: `
cluster:
  name: test-cluster
`,
            wantErr: true,
            errMsg:  "machine.type is required",
        },
        {
            name: "missing cluster name",
            config: `
machine:
  type: controlplane
`,
            wantErr: true,
            errMsg:  "cluster.name is required",
        },
        {
            name: "valid minimal config",
            config: `
machine:
  type: controlplane
cluster:
  name: test-cluster
`,
            wantErr: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            tmpDir, cleanup := testutil.TempDir(t)
            defer cleanup()

            configPath := testutil.TempFile(t, tmpDir, "config-*.yaml", []byte(tt.config))

            err := validateTalosConfig(configPath)
            if tt.wantErr {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tt.errMsg)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

## TDD Best Practices for HomeOps CLI

### 1. Start with the Interface

Define what you want the command to do before implementing:

```go
// Test the command interface first
func TestNewCommandStructure(t *testing.T) {
    cmd := newMyCommand()

    // Test command properties
    assert.Equal(t, "my-command", cmd.Use)
    assert.NotEmpty(t, cmd.Short)

    // Test flags
    assert.True(t, cmd.Flags().HasFlag("dry-run"))
    assert.True(t, cmd.Flags().HasFlag("namespace"))
}
```

### 2. Test Edge Cases Early

```go
func TestCommandEdgeCases(t *testing.T) {
    tests := []struct {
        name    string
        args    []string
        wantErr bool
    }{
        {"no args", []string{}, true},
        {"empty string arg", []string{""}, true},
        {"too many args", []string{"a", "b", "c"}, true},
        {"valid args", []string{"valid-input"}, false},
    }

    // Test implementation...
}
```

### 3. Use Test Helpers

```go
func TestWithTestEnvironment(t *testing.T) {
    // Use helpers to set up test environment
    cleanup := testutil.SetEnvs(t, map[string]string{
        "KUBECONFIG":     "/tmp/test-kubeconfig",
        "TALOS_VERSION":  "v1.8.0",
    })
    defer cleanup()

    // Test with environment set up
}
```

### 4. Mock External Dependencies

```go
func TestWithMockedDependencies(t *testing.T) {
    // Mock HTTP client
    mockClient := testutil.NewMockHTTPClient()
    mockClient.AddResponse("https://api.test.com", 200, `{"success": true}`)

    // Mock Kubernetes client
    k8sClient := testutil.MockKubernetesClient(
        testutil.CreateTestPod("test-pod", "default"),
        testutil.CreateTestNamespace("test-ns"),
    )

    // Test with mocks
}
```

## Troubleshooting TDD

### Common Issues

1. **Tests are slow**
   ```bash
   # Use short tests only
   go test -short ./...

   # Or run specific package
   go test ./cmd/talos
   ```

2. **Tests fail unexpectedly**
   ```bash
   # Run with verbose output
   make test-verbose

   # Run single test with detailed output
   go test -v -run TestSpecificTest ./cmd/talos
   ```

3. **TDD mode not working**
   ```bash
   # Check if entr is installed
   which entr

   # Install if missing (macOS)
   brew install entr

   # Manual watch command
   find . -name '*.go' | entr -c go test -v -short ./...
   ```

### Debug Failed Tests

```bash
# Run specific failing test
make test-specific TEST=TestFailingTest

# Run with race detection
go test -race -run TestFailingTest ./...

# Debug with delve
dlv test ./cmd/talos -- -test.run TestFailingTest
```

## Quick Reference

### Essential TDD Commands

```bash
# Start TDD mode
make test-tdd

# Run unit tests
make test-unit

# Run specific test
make test-specific TEST=TestName

# Test specific package
make test-package PKG=./cmd/talos

# Generate coverage
make test-coverage
```

### Test File Template

```go
package mypackage

import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/GizmoTickler/home-ops/cmd/homeops-cli/internal/testutil"
)

func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
        wantErr  bool
    }{
        {
            name:     "valid case",
            input:    "valid input",
            expected: "expected output",
            wantErr:  false,
        },
        {
            name:    "error case",
            input:   "invalid input",
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, err := myFunction(tt.input)

            if tt.wantErr {
                require.Error(t, err)
            } else {
                require.NoError(t, err)
                assert.Equal(t, tt.expected, result)
            }
        })
    }
}
```

## Next Steps

1. **Practice the workflow**: Start with simple functions and work up to complex commands
2. **Read existing tests**: Study the patterns in `cmd/bootstrap/bootstrap_test.go`
3. **Use TDD for new features**: Always write tests first for new functionality
4. **Maintain test coverage**: Aim for >80% coverage with `make test-coverage-threshold`

For more detailed information, see the full [Testing Guide](TESTING.md).