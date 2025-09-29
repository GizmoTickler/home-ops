# Repository Guidelines

## Project Structure & Module Organization
Home Ops pairs Go utilities with GitOps manifests. `cmd/homeops-cli` hosts the main CLI (commands in `cmd/`, shared logic in `internal/`, docs and fixtures in `docs/` and `testdata/`). `cmd/truenas-deep-explorer` mirrors the layout for storage tooling. Kubernetes configuration lives in `kubernetes`: `apps/<namespace>/<app>` for workload releases, `components/` for cluster services, and `flux/` for Flux sources, Kustomizations, and SOPS-encrypted secrets.

## Build, Test, and Development Commands
Work from each Go module directory. `make build` compiles a versioned binary, `make test` runs race-enabled Go tests, and `make fmt` applies gofmt. Use `make lint` after installing `golangci-lint`, `make check` to chain fmt/vet/lint/test, and `make test-integration` for `//go:build integration` suites that expect a reachable cluster. `make run` or `make demo` provide quick CLI smoke tests.

## Coding Style & Naming Conventions
Keep Go sources gofmt-clean with tab indentation and lower-case package names; CLI command names stay kebab-case. Update `cmd/homeops-cli/COMMANDS.md` whenever a user-facing verb changes. For YAML, use two-space indentation, keep directory names `<namespace>/<app>`, and align resource names with their folders (for example, `HelmRelease` `home-assistant`). Leave secrets encrypted; never commit decrypted files.

## Testing Guidelines
Add unit tests in neighbouring `*_test.go` files with descriptive names like `TestTalosVersion`. Expensive scenarios belong behind the `integration` build tag and run via `make test-integration`. Maintain ≥80% coverage by checking `make test-coverage-threshold`; keep `coverage.out` local. For manifests, verify rendering with `kustomize build kubernetes/apps/<namespace>/<app>` or the Flux CLI (`flux diff kustomization --path kubernetes/...`) before review.

## Commit & Pull Request Guidelines
Commit messages follow Conventional Commits (`type(scope): subject`), using scopes that map to folders (`feat(container): ...`, `chore(cli): ...`). Mark breaking changes with `!` and explain them in the PR. PR descriptions should summarise intent, note the commands or diffs run, link issues or Renovate updates, and include screenshots when dashboards or manifests change. Re-run `sops -e` on touched secrets prior to pushing.

## Security & Configuration Tips
Keep Age keys local (`export SOPS_AGE_KEY_FILE=age.key`) and out of Git. Use `sops -d` only for short-lived inspections and delete any decrypted artefacts immediately. Apply cluster changes through Flux rather than manual `kubectl`, and document Talos endpoints or credentials in module READMEs instead of source files.
