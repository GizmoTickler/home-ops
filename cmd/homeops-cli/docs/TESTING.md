# Testing Guide for HomeOps CLI

This document provides comprehensive guidance for testing the HomeOps CLI, including test-driven development (TDD) practices, test structure, and running tests.

## Table of Contents

- [Overview](#overview)
- [Test Structure](#test-structure)
- [Test Types](#test-types)
- [Running Tests](#running-tests)
- [Test-Driven Development (TDD)](#test-driven-development-tdd)
- [Writing Tests](#writing-tests)
- [Test Helpers and Mocks](#test-helpers-and-mocks)
- [Continuous Integration](#continuous-integration)
- [Best Practices](#best-practices)

## Overview

The HomeOps CLI uses a comprehensive testing strategy that includes:

- **Unit Tests**: Test individual functions and components in isolation
- **Integration Tests**: Test component interactions and workflows
- **Benchmark Tests**: Performance testing for critical operations
- **End-to-End Tests**: Full workflow testing with real or simulated infrastructure

## Test Structure

```
cmd/homeops-cli/
├── internal/testutil/          # Test utilities and helpers
│   ├── helpers.go             # Common test helpers
│   └── mocks.go               # Mock implementations
├── cmd/
│   ├── bootstrap/
│   │   ├── bootstrap_test.go   # Unit tests
│   │   └── integration_test.go # Integration tests
│   ├── talos/
│   │   └── talos_test.go      # Unit tests
│   ├── kubernetes/
│   │   └── kubernetes_test.go # Unit tests
│   └── volsync/
│       └── volsync_test.go    # Unit tests
├── integration_test.go        # System-wide integration tests
└── docs/
    └── TESTING.md            # This file
```

## Test Types

### Unit Tests

Unit tests focus on testing individual functions and methods in isolation. They use mocks and stubs to eliminate external dependencies.

**Example:**
```go
func TestValidateVMName(t *testing.T) {
    tests := []struct {
        name    string
        vmName  string
        wantErr bool
        errMsg  string
    }{
        {
            name:    "valid name",
            vmName:  "test_vm",
            wantErr: false,
        },
        {
            name:    "invalid name with dashes",
            vmName:  "test-vm",
            wantErr: true,
            errMsg:  "cannot contain dashes",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validateVMName(tt.vmName)
            if tt.wantErr {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tt.errMsg)
            } else {
                require.NoError(t, err)
            }
        })
    }
}
```

### Integration Tests

Integration tests verify that components work correctly together. They may use real or simulated external services.

**Running Integration Tests:**
```bash
make test-integration
```

### Benchmark Tests

Benchmark tests measure performance of critical operations.

**Example:**
```go
func BenchmarkConfigRendering(b *testing.B) {
    for i := 0; i < b.N; i++ {
        _, _ = renderMachineConfig("192.168.1.10", "controlplane")
    }
}
```

## Running Tests

### Basic Test Commands

```bash
# Run all tests
make test

# Run only unit tests (short tests)
make test-unit

# Run integration tests
make test-integration

# Run tests with coverage
make test-coverage

# Run benchmarks
make benchmark

# Clean test artifacts
make test-clean
```

### TDD Workflow Commands

```bash
# Start TDD mode (watch and run tests on changes)
make test-tdd

# Run tests in watch mode
make test-watch

# Run specific test
make test-specific TEST=TestValidateVMName

# Test specific package
make test-package PKG=./cmd/talos
```

### Coverage Commands

```bash
# Generate coverage report
make test-coverage

# Check coverage threshold (80%)
make test-coverage-threshold
```

## Test-Driven Development (TDD)

The HomeOps CLI follows TDD practices for new feature development. Here's the recommended workflow:

### 1. Red - Write a Failing Test

Start by writing a test that describes the behavior you want to implement:

```go
func TestNewFeature(t *testing.T) {
    // Test for a feature that doesn't exist yet
    result := myNewFunction("input")
    assert.Equal(t, "expected", result)
}
```

### 2. Green - Make the Test Pass

Implement the minimal code needed to make the test pass:

```go
func myNewFunction(input string) string {
    // Minimal implementation
    return "expected"
}
```

### 3. Refactor - Improve the Code

Refactor the implementation while keeping the test passing:

```go
func myNewFunction(input string) string {
    // Improved implementation
    return processInput(input)
}
```

### 4. TDD Workflow with Make Commands

```bash
# Start TDD mode
make test-tdd

# This will:
# 1. Watch for file changes
# 2. Automatically run tests when files change
# 3. Show immediate feedback
# 4. Clear screen between runs for better visibility
```

### 5. Example TDD Session

1. **Write failing test:**
   ```bash
   # Edit cmd/talos/talos_test.go
   # Add TestNewVMDeployment function
   ```

2. **Run tests (should fail):**
   ```bash
   make test-package PKG=./cmd/talos
   ```

3. **Implement minimal feature:**
   ```bash
   # Edit cmd/talos/talos.go
   # Add newVMDeployment function
   ```

4. **Run tests (should pass):**
   ```bash
   make test-package PKG=./cmd/talos
   ```

5. **Refactor and repeat**

## Writing Tests

### Test Naming Conventions

- Test files: `*_test.go`
- Test functions: `TestFunctionName`
- Benchmark functions: `BenchmarkFunctionName`
- Integration tests: Include `// +build integration` tag

### Test Structure

Use the Arrange-Act-Assert pattern:

```go
func TestExample(t *testing.T) {
    // Arrange
    input := "test input"
    expected := "expected output"

    // Act
    result := functionUnderTest(input)

    // Assert
    assert.Equal(t, expected, result)
}
```

### Table-Driven Tests

For testing multiple scenarios:

```go
func TestMultipleScenarios(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
        wantErr  bool
    }{
        {
            name:     "valid input",
            input:    "valid",
            expected: "processed",
            wantErr:  false,
        },
        {
            name:    "invalid input",
            input:   "invalid",
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, err := functionUnderTest(tt.input)

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

### Environment Setup in Tests

Use test helpers for environment setup:

```go
func TestWithEnvironment(t *testing.T) {
    // Use helper to set environment variables
    cleanup := testutil.SetEnvs(t, map[string]string{
        "TEST_VAR": "test_value",
    })
    defer cleanup()

    // Test code here
}
```

## Test Helpers and Mocks

### Test Utilities (`internal/testutil/`)

#### Helpers

- `TempDir(t)`: Create temporary directory
- `TempFile(t, dir, pattern, content)`: Create temporary file
- `SetEnv(t, key, value)`: Set environment variable with cleanup
- `SetEnvs(t, envs)`: Set multiple environment variables
- `ExecuteCommand(cmd, args...)`: Execute cobra command
- `CaptureOutput(func())`: Capture stdout/stderr

#### Mocks

- `MockHTTPClient`: HTTP client mock
- `MockKubernetesClient`: Kubernetes client mock
- `MockTalosClient`: Talos client mock
- `MockTrueNASClient`: TrueNAS client mock
- `Mock1PasswordClient`: 1Password client mock
- `MockCommandExecutor`: Command execution mock

### Using Mocks

```go
func TestWithMocks(t *testing.T) {
    // Create mock HTTP client
    mockClient := testutil.NewMockHTTPClient()
    mockClient.AddResponse("https://api.example.com", 200, `{"result": "success"}`)

    // Use mock in test
    // (Requires dependency injection in production code)
}
```

## Continuous Integration

### GitHub Actions Integration

The test suite integrates with CI/CD pipelines:

```yaml
# .github/workflows/test.yml
name: Tests
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v3
        with:
          go-version: '1.21'
      - run: make test-coverage
      - run: make test-coverage-threshold
```

### Pre-commit Hooks

Set up pre-commit hooks to run tests before commits:

```bash
# .git/hooks/pre-commit
#!/bin/sh
make test-unit
```

## Best Practices

### 1. Test Organization

- **One test file per source file**: `foo.go` → `foo_test.go`
- **Group related tests**: Use subtests with `t.Run()`
- **Clear test names**: Describe what is being tested and expected outcome

### 2. Test Independence

- **No test dependencies**: Each test should be independent
- **Clean up resources**: Use `defer` for cleanup
- **Isolated state**: Don't rely on global state

### 3. Test Data

- **Use table-driven tests**: For multiple input/output scenarios
- **Meaningful test data**: Use realistic data that represents actual use cases
- **Edge cases**: Test boundary conditions and error scenarios

### 4. Assertions

- **Use testify**: Prefer `require` for critical assertions, `assert` for non-critical
- **Specific assertions**: Use appropriate assertion methods (`Equal`, `Contains`, etc.)
- **Good error messages**: Provide context for failed assertions

### 5. Mocking Strategy

- **Mock external dependencies**: HTTP clients, databases, file systems
- **Interface-based mocking**: Design interfaces for mockable dependencies
- **Minimal mocking**: Only mock what's necessary for the test

### 6. Performance Testing

- **Benchmark critical paths**: Focus on performance-sensitive operations
- **Consistent benchmarks**: Run benchmarks multiple times
- **Track performance**: Monitor benchmark results over time

### 7. Integration Testing

- **Test real workflows**: Verify end-to-end functionality
- **Use test environments**: Separate test infrastructure from production
- **Cleanup test resources**: Remove test data after tests complete

## Troubleshooting

### Common Issues

1. **Tests fail in CI but pass locally**
   - Check environment variables
   - Verify dependencies are available
   - Review file paths (absolute vs relative)

2. **Flaky tests**
   - Identify race conditions
   - Add proper synchronization
   - Use deterministic test data

3. **Slow tests**
   - Optimize test setup/teardown
   - Use shorter timeouts for unit tests
   - Parallel test execution where appropriate

### Debugging Tests

```bash
# Run tests with verbose output
make test-verbose

# Run specific failing test
make test-specific TEST=TestFailingTest

# Debug with delve
dlv test ./cmd/talos -- -test.run TestSpecificTest
```

## Resources

- [Go Testing Package](https://pkg.go.dev/testing)
- [Testify Documentation](https://github.com/stretchr/testify)
- [TDD Best Practices](https://martinfowler.com/articles/practical-test-pyramid.html)
- [Go Test Examples](https://golang.org/doc/tutorial/add-a-test)

## Contributing

When adding new features:

1. **Start with tests**: Write tests before implementation (TDD)
2. **Maintain coverage**: Aim for >80% test coverage
3. **Add integration tests**: For new workflows or command interactions
4. **Update documentation**: Keep this guide updated with new patterns

For questions about testing practices, refer to the team's development guidelines or create an issue in the repository.