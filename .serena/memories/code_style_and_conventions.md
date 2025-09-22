# Code Style and Conventions

## Go Code Style
- **Formatting**: Use `gofmt` via `make fmt`
- **Linting**: golangci-lint configuration in `.golangci.yml`
- **Error Handling**: Wrap errors with context using `fmt.Errorf`
- **Logging**: Use zap structured logging from `internal/common/logger.go`
- **Naming**: Follow Go conventions (CamelCase for exported, camelCase for unexported)

## Testing Conventions
- **Test Framework**: testify/assert and testify/require
- **Test Files**: `*_test.go` alongside source files
- **Test Structure**: Table-driven tests with subtests using `t.Run()`
- **Mocking**: Use testify for assertions, custom mocks in `internal/testutil/`
- **Coverage**: Target 80% coverage threshold
- **Integration Tests**: Use build tag `integration`

## Project Structure Patterns
- **Commands**: Cobra commands in `cmd/` directory
- **Internal Packages**: Business logic in `internal/`
- **Templates**: Embedded via `go:embed` in `internal/templates/`
- **Configuration**: Viper-based config in `internal/config/`

## Error Handling Patterns
- Structured error handling with context
- Use `internal/errors/` package for error types
- Implement retry logic for network operations
- Validate inputs early with clear error messages

## Template Development
- Jinja2 syntax with environment variable substitution
- 1Password references: `op://vault/item/field` format
- Test template rendering in isolation
- Templates embedded in binary via go:embed

## Documentation
- **Comments**: Package-level documentation required
- **Function docs**: For exported functions only
- **Examples**: Include usage examples in tests
- **README**: Keep up-to-date with current functionality

## Security Practices
- Never commit secrets or keys to repository
- Use 1Password for secret management
- Validate all inputs
- Use SOPS with age encryption for Git-stored secrets

## Git Workflow
- Commit message format: `feat/fix: descriptive message`
- Always commit YAML changes for GitOps
- Use conventional commit types