# Repository Guidelines

## Project Structure & Module Organization
- CLI sources live in `cmd/homeops-cli`; subcommands stay under `cmd/`, shared logic in `internal/`, and docs or fixtures in `docs/` and `testdata/`.
- Storage tooling sits in `cmd/truenas-deep-explorer` with the same layout.
- Kubernetes manifests reside in `kubernetes/`: workloads in `apps/<namespace>/<app>`, shared services in `components/`, and Flux sources plus Kustomizations in `flux/`.
- Specs and proposals are tracked in `openspec/`; run `openspec list` before planning broad changes.

## Build, Test, and Development Commands
- `make build` packages a versioned binary for the current module.
- `make fmt` runs gofmt; `make lint` invokes golangci-lint (install it first).
- `make test` executes race-enabled unit tests; `make test-integration` runs suites gated by `//go:build integration`.
- `make check` chains fmt, vet, lint, and test; `make run` or `make demo` provide quick CLI smoke exercises.

## Coding Style & Naming Conventions
- Go code must remain gofmt-clean with tab indentation; package names stay lower-case.
- CLI verbs are kebab-case (e.g., `homeops-cli status`), and exported flags follow Go naming conventions.
- YAML manifests use two-space indentation and align directory names with Kubernetes resource names (e.g., `apps/home/home-assistant/HelmRelease`).

## Testing Guidelines
- Place unit tests beside sources as `*_test.go` with names like `TestTalosVersion`.
- Maintain ≥80% coverage; use `make test-coverage-threshold` to verify before pushing.
- Prefer table-driven tests for command behavior; keep expensive calls behind the `integration` tag and run via `make test-integration`.

## Commit & Pull Request Guidelines
- Follow Conventional Commits (`type(scope): subject`), aligning scopes to directories (`feat(cli)`, `chore(kubernetes/apps/home)`).
- Flag breaking changes with `!` and describe them in the PR summary.
- PRs should cover intent, commands run, linked issues or Renovate entries, and screenshots when manifests alter dashboards.

## Security & Configuration Tips
- Keep Age keys local (`SOPS_AGE_KEY_FILE=age.key`) and never commit decrypted secrets.
- Use `sops -d` only for short inspections and delete outputs promptly.
- Apply cluster changes through Flux and document Talos endpoints or credentials in README files instead of source code.
