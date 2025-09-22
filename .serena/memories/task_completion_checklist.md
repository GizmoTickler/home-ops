# Task Completion Checklist

## When Completing Any Coding Task

### 1. Code Quality Checks
```bash
make fmt          # Format code
make vet          # Run go vet  
make lint         # Run golangci-lint (if available)
```

### 2. Testing Requirements
```bash
make test                    # Run all tests
make test-coverage          # Generate coverage report
make test-coverage-threshold # Ensure 80% coverage
```

### 3. Build Verification
```bash
make build       # Ensure binary builds successfully
make check       # Run all checks (fmt, vet, lint, test)
```

### 4. Integration Testing (if applicable)
```bash
make test-integration   # Run integration tests with proper tags
```

### 5. Documentation Updates
- Update function/package documentation if interfaces changed
- Update COMMANDS.md if CLI commands were modified
- Ensure examples in code are current

### 6. Git Workflow (for configuration changes)
```bash
git add -A
git commit -m "feat/fix: descriptive message"
git push
# For Kubernetes changes:
flux reconcile source git flux-system -n flux-system
```

### 7. Performance Considerations
```bash
make benchmark   # Run benchmarks if performance-sensitive code
```

### 8. Security Review
- Verify no secrets are hardcoded
- Ensure 1Password integration works correctly
- Check input validation is adequate

### 9. Template Testing (if templates modified)
- Test template rendering with dry-run mode
- Verify 1Password references resolve correctly
- Validate environment variable substitution

### 10. Final Verification
```bash
make dev         # Quick development cycle check
./homeops-cli --help  # Verify CLI still works
```

## Special Considerations

### For CLI Command Changes
- Test help output: `./homeops-cli <command> --help`
- Verify shell completion still works
- Update command documentation

### For Internal Package Changes  
- Run package-specific tests: `make test-package PKG=./internal/<package>`
- Check for breaking changes in interfaces
- Update internal documentation

### For Template Changes
- Test template rendering in isolation
- Verify embedded templates are included in binary
- Test with actual environment variables/1Password references