# HomeOps CLI Coverage Review

Updated: 2026-04-01

## Current `go test -short ./...` package coverage

| Package | Coverage |
| --- | ---: |
| `homeops-cli` | 73.4% |
| `cmd/bootstrap` | 71.5% |
| `cmd/completion` | 73.3% |
| `cmd/kubernetes` | 75.0% |
| `cmd/talos` | 70.1% |
| `cmd/volsync` | 73.1% |
| `cmd/workstation` | 78.7% |
| `internal/common` | 85.9% |
| `internal/config` | 90.7% |
| `internal/errors` | 97.5% |
| `internal/iso` | 85.5% |
| `internal/metrics` | 98.4% |
| `internal/proxmox` | 72.1% |
| `internal/security` | 88.4% |
| `internal/ssh` | 90.2% |
| `internal/talos` | 83.3% |
| `internal/template` | 74.0% |
| `internal/templates` | 82.8% |
| `internal/testutil` | 85.3% |
| `internal/truenas` | 76.8% |
| `internal/ui` | 76.6% |
| `internal/vsphere` | 70.2% |
| `internal/yaml` | 79.9% |
| `internal/constants` | covered, no statements |

## Packages At Or Above 70%

- `cmd/completion`
- `cmd/bootstrap`
- `homeops-cli`
- `cmd/kubernetes`
- `cmd/volsync`
- `cmd/workstation`
- `internal/common`
- `internal/config`
- `internal/constants`
- `internal/errors`
- `internal/iso`
- `internal/metrics`
- `internal/proxmox`
- `internal/security`
- `internal/ssh`
- `internal/talos`
- `internal/template`
- `internal/templates`
- `internal/testutil`
- `internal/truenas`
- `internal/ui`
- `internal/vsphere`
- `internal/yaml`

## Highest-Value Remaining Gaps

- `cmd/talos`, `cmd/kubernetes`, and `cmd/bootstrap` are still dominated by direct `exec.Command` calls to external CLIs such as `talosctl`, `kubectl`, `flux`, `helmfile`, `ssh`, and `op`. Coverage is currently constrained by that coupling more than by missing test cases.
- `cmd/bootstrap` now clears the package target on its direct run, but it still depends heavily on injected seams around `talosctl`, `kubectl`, `helmfile`, DNS, HTTP, and 1Password. The remaining uncovered surface is mostly live binary execution glue rather than pure decision logic.
- `cmd/kubernetes` now clears the package target directly at `75.0%`, but most of the remaining uncovered surface is still wrapper-heavy CLI execution and interactive selection plumbing.
- `homeops-cli` and `internal/vsphere` are no longer structural outliers, but both got there via helper extraction and seams rather than broad live-integration tests. The next gains now depend on refactoring the command packages the same way.
- `internal/constants` now has direct tests, but it still reports “no statements” because the package is declaration-only. That package should be treated as structurally exempt from percentage-based coverage targets.

## Concrete Improvement Opportunities

### `cmd/kubernetes`

- Extract a command runner interface for `kubectl`, `git`, and `flux` calls.
- Split the command package into “parse/build arguments” helpers and “execute CLI” adapters.
- Extend `parseKustomizationFile` callers so multi-document `ks.yaml` files can select a specific Flux Kustomization by name instead of implicitly taking the first complete document.

### `cmd/talos`

- Continue separating provider-specific orchestration from Cobra command wiring.
- Push the same helper/plan structure now used in deploy and dry-run flows into any remaining provider-specific management paths.
- Keep reducing direct CLI coupling around `talosctl`, SSH, and 1Password-backed runtime access.
- Normalize final user-facing success/reporting output across providers now that execution structure is much more aligned.

### `cmd/volsync`

- Continue extracting the remaining restore/list flows into smaller plan/build/apply helpers.
- Collapse repeated `kubectl get replicationsources/... --output=jsonpath=...` calls into shared fetchers so restore configuration is validated in one place.
- Add a small command abstraction for the remaining `flux` and `kubectl apply` paths to reduce shell-coupling in restore flows.

### `cmd/bootstrap`

- Extract preflight checks into individual check objects with injectable command/filesystem/network dependencies.
- Replace long procedural functions with smaller validator/render/apply helpers that can be tested without Talos or Kubernetes binaries.
- Centralize YAML and 1Password reference validation so coverage accumulates in reusable packages rather than only in bootstrap.

### `internal/vsphere`

- Introduce seams around govmomi finder/resource objects and task waiters.
- Split VM spec generation from API submission so device/spec building can be tested without a live vSphere client.
- Wrap `op` and `ssh` execution behind injectable functions instead of calling shell commands directly.

### `internal/proxmox`

- Add lightweight interfaces around cluster/node/storage/task operations.
- Extract VM listing and formatting/output logic into pure helpers.
- Move task waiting and VM lookup logic behind fakeable adapters.

### `internal/truenas`

- Extend the `callFn` seam or introduce an interface so all RPC consumers use the same injectable path.
- Split ZVol discovery/deletion planning from RPC invocation.
- Add tests for VM lookup, delete flows, and dataset verification using the existing seam.

## Specific Findings From This Coverage Push

- `cmd/bootstrap` had two real readiness bugs: partial deployment readiness like `1/2` could be treated as ready, and repeated `kubectl` failures in node/webhook/controller polling could burn through long max-wait windows instead of failing as a stalled bootstrap. The package now has helper-based readiness parsing plus injectable polling seams for these paths.
- `cmd/bootstrap` now also has direct coverage over bootstrap command wiring, interactive option selection, namespace creation, ClusterSecretStore application, CRD readiness polling, external-secrets detection, Flux reconciliation sequencing, and dynamic Helmfile values validation. It is no longer a near-empty shell around untestable command execution.
- `cmd/bootstrap` also gained direct coverage over Talos node discovery retries, per-node config application flow, Talos bootstrap retry logic, kubeconfig fetch/patch paths, CRD-only helmfile extraction, dynamic helmfile sync staging, CRD metadata repair, and rendered Talos config assembly. Direct package coverage is now `71.5%`.
- `cmd/bootstrap` test cleanup was sharing global temp-directory state during suite runs. The Talos merge temp files now use a package temp-dir seam in tests so the cleanup assertion is deterministic under repo-wide runs.
- `cmd/workstation` previously had very little real behavioral coverage around brew and krew orchestration. It now has injectable seams around CLI checks, brew bundle checks, krew installation/update, and plugin install decisions, which raised the package above the target without relying on shell-heavy tests.
- `cmd/kubernetes/parseKustomizationFile` previously used brittle line-based parsing. It now uses YAML decoding, which is safer, and the package gained real coverage around interactive PVC browsing, node-shell flows, Flux sync selection, and Kustomization render/apply/delete behavior. The remaining shortfall is now mostly in wrapper and output-format branches.
- `cmd/kubernetes` had a nondeterministic parallel reconcile test that was asserting goroutine completion order. That test now validates the expected reconcile set without depending on scheduling order, which restored stable repo-wide `go test -short ./...` runs.
- `cmd/kubernetes` is now above target on its direct package run at `75.0%`. The latest gains came from real branch coverage in secret rendering formats, Flux sync cancellation/validation, multi-document Kustomization selection, and command-wrapper confirmation paths.
- `cmd/volsync/parseKopiaSnapshots` had a real token indexing bug in snapshot parsing. Tests exposed that and the parser now reads timestamp, snapshot ID, and size from the correct fields.
- `cmd/volsync` is now above target after moving snapshot, restore, namespace-selection, template-render, and CLI execution paths behind narrow seams. That work also improved real behavior by preserving deterministic snapshot trigger timestamps in tests, surfacing patch errors with context, and making repo-wide test execution stable instead of shell- and temp-dir-sensitive.
- `cmd/talos` now meets the package target. Coverage includes provider normalization and dispatch, dry-run deployment previews, Talos node selection, config parsing, 1Password signin retry during config apply, upgrade/reboot/reset/shutdown command assembly, kubeconfig generation/push/pull flows, interactive deploy prompt paths, ISO preparation routing, ISO download handling, template schematic updates, and hypervisor wrapper operations for TrueNAS, Proxmox, and vSphere.
- `cmd/talos` also landed substantial structural cleanup beyond raw coverage:
  - Proxmox multi-VM deploys now support bounded concurrency and `start-index` naming
  - the Proxmox storage-format bug that generated invalid disk specs like `efidisk0=:1` and `scsi0=:5` is fixed
  - `deploy-vm --provider` now normalizes aliases such as `esxi` and rejects invalid providers instead of silently falling into the wrong deploy path
  - dry-run preview generation now lives in pure summary builders for TrueNAS, Proxmox, and vSphere
  - live deploy flows now use plan/config helpers for Proxmox, vSphere, and TrueNAS instead of assembling everything inline
  - the TrueNAS deploy path now uses seams for SSH verification, ISO selection, VM-manager creation, spinner execution, and success reporting
- The remaining uncovered surface in `cmd/talos` is now much more concentrated in live external-command/provider behavior rather than basic branching or prompt handling.
- Several credential-resolution tests in `cmd/talos`, `internal/proxmox`, and `internal/truenas` were unintentionally reading live 1Password-backed credentials from the environment. Those tests are now hermetic again by shadowing `op` in-process so they reliably exercise the intended env-fallback behavior during suite runs.
- `internal/templates` renderer detection had an ordering bug where `{{ ENV.* }}` content could be misclassified as a Go template before Jinja-style handling.
- `internal/talos` factory coverage improved substantially once the tests exercised the actual HTTP orchestration path via fake `RoundTripper` implementations instead of relying on local listeners.
- `internal/truenas` was effectively blocked for deeper unit testing until an RPC seam was added to `WorkingClient`. With that seam in place, deploy/list/info/delete flows can now be covered as normal unit tests.
- `internal/proxmox` improved once VM deployment, list/info formatting, VM name lookup, and ISO upload paths were pulled behind narrow seams. It also exposed a real robustness issue: uninitialized clients now return explicit errors instead of nil-dereferencing in `Connect` and `GetNextVMID`.
- `internal/iso` now has real downloader-path coverage using an injected SSH client seam, including validation failures, empty downloads, missing files, and the successful custom ISO flow.
- `internal/vsphere` is now above target after splitting VM spec/device building, retry logic, and ESXi command execution behind narrow helpers and seams. That work also fixed two real robustness issues: ESXi shell commands now quote datastore paths safely, and VM registration output is parsed defensively instead of assuming a raw numeric line.
- The root `homeops-cli` package moved above target after extracting the signal-aware entrypoint flow into testable helpers and covering the interactive menu behavior instead of leaving the Cobra entrypoint as an untested `main` wrapper.
- `internal/ui` now has coverage for both fallback and gum-backed paths, but terminal reset and TTY behavior are still inherently environment-sensitive.

## Recommended Next Step

- With the package target now cleared across the meaningful packages and repo-wide `go test -short ./...` green again, the next work should shift from raw percentage chasing to structural cleanup:
  1. keep extracting CLI-heavy flows in `cmd/bootstrap`, `cmd/talos`, and `cmd/kubernetes` into testable helpers
  2. treat `internal/constants` as declaration-only and exempt from percentage-based gating
  3. continue the deeper functionality pass on interactive and provider-specific workflows rather than adding low-signal tests
  4. update the review/docs trail as structural bugs and refactors land so the repository guidance matches the current implementation instead of the pre-refactor state
