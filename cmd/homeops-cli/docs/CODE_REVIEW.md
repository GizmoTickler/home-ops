# Homeops-CLI Code Review and Fixes

This document tracks identified issues in the homeops-cli codebase and their resolutions.

## Summary

| Issue | Severity | Status | Location |
|-------|----------|--------|----------|
| Interactive menu string slicing panic | High | Fixed | `main.go:97-105` |
| Broken Krew installation | High | Fixed | `workstation.go:174-181` |
| Inconsistent restore-all confirmation | Medium | Fixed | `volsync.go:994-1001` |
| Inconsistent shutdown/reset confirmation | Medium | Fixed | `talos.go:649-656, 780-786` |
| Double power-on in vSphere | Medium | Fixed | `vsphere/client.go:654-668` |
| Secret key shell escaping | Medium | Fixed | `kubernetes.go:542-544` |
| Hardcoded truenas-csi namespace | Medium | Fixed | `bootstrap.go:403` |
| Duplicated ks.yaml parsing | Low | Fixed | `kubernetes.go` |
| Inconsistent stderr logging | Medium | Fixed | `talos.go`, `workstation.go` |
| No global log level control | Low | Fixed | `main.go`, `logger.go` |
| Per-function logger creation | Low | Improved | `logger.go` |
| Proxmox disk storage format bug | High | Fixed | `internal/proxmox/vm_manager.go` |
| `deploy-vm` invalid provider fallback to vSphere | Medium | Fixed | `cmd/talos/talos.go` |
| Batch deploy naming lacked start index control | Medium | Fixed | `cmd/talos/talos.go` |
| TrueNAS/vSphere/Proxmox deploy orchestration drift | Medium | Improved | `cmd/talos/talos.go` |

---

## Detailed Issues and Fixes

### 1. Interactive Menu String Slicing Panic

**Location**: `main.go:97-105`

**Problem**:
```go
switch {
case selected[:9] == "bootstrap":
case selected[:3] == "k8s":
...
}
```
Using slice indexing on strings can cause index-out-of-range panics if the string is shorter than expected.

**Fix**: Use `strings.HasPrefix()` instead of slice indexing.

**Status**: ✅ Fixed

---

### 2. Broken Krew Installation

**Location**: `workstation.go:174-181`

**Problem**:
```go
func installKrew() error {
    cmd := exec.Command("kubectl", "krew", "install", "krew")
    return cmd.Run()
}
```
This creates a circular dependency - trying to use krew to install krew when krew isn't installed.

**Fix**: Implement proper krew installation using the official installation method.

**Status**: ✅ Fixed

---

### 3. Inconsistent restore-all Confirmation

**Location**: `volsync.go:994-1001`

**Problem**:
Uses raw `fmt.Scanln` instead of the `ui.Confirm` pattern, bypassing the gum fallback system.

**Fix**: Replace with `ui.Confirm()` call.

**Status**: ✅ Fixed

---

### 4. Inconsistent shutdown/reset Cluster Confirmation

**Location**: `talos.go:649-656, 780-786`

**Problem**:
Same as above - uses raw `fmt.Scanln` instead of `ui.Confirm`.

**Fix**: Replace with `ui.Confirm()` calls.

**Status**: ✅ Fixed

---

### 5. Double Power-On in vSphere

**Location**: `vsphere/client.go:654-668`

**Problem**:
`CreateVM()` already handles power-on with retry logic. Then `DeployVMsConcurrently()` calls `PowerOnVM()` again, causing double power-on attempts.

**Fix**: Remove the redundant power-on call from `DeployVMsConcurrently()` since `CreateVM()` already handles it.

**Status**: ✅ Fixed

---

### 6. Secret Key Shell Escaping

**Location**: `kubernetes.go:542-544`

**Problem**:
```go
valueCmd := exec.Command("bash", "-c",
    fmt.Sprintf("kubectl get secret %s -n %s -o go-template='{{index .data \"%s\"}}'", secretName, namespace, k))
```
Secret keys containing special characters could break the command or cause security issues.

**Fix**: Use proper kubectl arguments with `--` separator and proper escaping.

**Status**: ✅ Fixed

---

### 7. Hardcoded truenas-csi Namespace

**Location**: `bootstrap.go:403`

**Problem**:
The namespace list includes `truenas-csi` but this has been replaced with `scale-csi` in the repository.

**Fix**: Update the namespace list to reflect current infrastructure.

**Status**: ✅ Fixed

---

### 8. Duplicated ks.yaml Parsing

**Location**: `kubernetes.go` (multiple functions)

**Problem**:
The pattern for extracting `name`, `namespace`, and `path` from ks.yaml files is duplicated across:
- `renderKustomization()`
- `applyKustomization()`
- `deleteKustomization()`

**Fix**: Extract into a common helper function `parseKustomizationFile()`.

**Status**: ✅ Fixed

---

## Testing

After applying fixes, run:

```bash
cd cmd/homeops-cli
make check  # Run all checks (fmt, vet, lint, test)
make build  # Build the binary
```

---

### 9. Inconsistent stderr Logging

**Location**: `talos.go` (5 occurrences), `workstation.go` (2 occurrences)

**Problem**:
```go
fmt.Fprintf(os.Stderr, "Warning: failed to close VM manager: %v\n", closeErr)
```
Multiple places use raw `fmt.Fprintf` to stderr instead of using the ColorLogger's `Warn()` method, which:
- Bypasses quiet mode
- Is inconsistent with the rest of the codebase
- Loses timestamp formatting

**Fix**: Replace all `fmt.Fprintf(os.Stderr, "Warning: ...")` with `logger.Warn(...)`.

**Status**: ✅ Fixed

---

### 10. No Global Log Level Control

**Location**: `main.go`, `internal/common/logger.go`

**Problem**:
- Log level could only be set via environment variables (`DEBUG=1` or `LOG_LEVEL=debug`)
- No CLI flag to control verbosity
- Each function created its own logger, re-reading env vars each time

**Fix**:
- Added `--log-level` global flag to root command
- Added `SetGlobalLogLevel()` and `GetGlobalLogLevel()` functions
- Added `Logger()` singleton function for efficient logger reuse

**Status**: ✅ Fixed

---

### 11. Per-Function Logger Creation (Improvement)

**Location**: `internal/common/logger.go`

**Problem**:
Every function calls `logger := common.NewColorLogger()`, creating a new instance each time.

**Improvement**:
- Added `common.Logger()` singleton function that returns a global logger instance
- Existing `NewColorLogger()` still works for backwards compatibility
- New code can use `common.Logger()` for efficiency

**Status**: ✅ Improved

---

### 12. Proxmox Disk Storage Format Bug

**Location**: `internal/proxmox/vm_manager.go`

**Problem**:
Custom Proxmox VM creation could emit invalid disk references such as:

```text
efidisk0=:1
scsi0=:5
```

That produced real API failures during VM creation:

```text
Parameter verification failed
unable to parse volume ID ':1'
unable to parse volume ID ':5'
```

**Fix**:
- Normalize storage selection before disk option assembly
- Ensure EFI and boot disk references always include a valid storage target
- Cover the storage-path behavior with direct Proxmox tests

**Status**: ✅ Fixed

---

### 13. `deploy-vm` Invalid Provider Fallback

**Location**: `cmd/talos/talos.go`

**Problem**:
`deploy-vm` accepted `--provider` values without normalizing or validating them before dispatch. Invalid values could silently fall through into the vSphere path instead of failing fast.

**Fix**:
- Normalize `--provider` through the shared provider parser before deployment dispatch
- Accept `esxi` as an explicit alias for `vsphere`
- Reject unsupported providers with a clear error

**Status**: ✅ Fixed

---

### 14. Batch Deploy Naming Missing Start Index

**Location**: `cmd/talos/talos.go`

**Problem**:
Batch Talos VM deploys always started naming at `-0`, which made iterative cluster bring-up and test deployments slower and more error-prone when users wanted `worker-3`, `worker-4`, etc.

**Fix**:
- Added `--start-index` support for batch deploys
- Applied the offset consistently across Proxmox and vSphere paths
- Updated dry-run and live deployment flows to use the same naming logic
- Improved interactive prompts so `start-index` is only asked for true batch deploys

**Status**: ✅ Fixed

---

### 15. Talos Provider Deploy Orchestration Drift

**Location**: `cmd/talos/talos.go`

**Problem**:
TrueNAS, Proxmox, and vSphere deploy flows had drifted into separate inline implementations mixing:
- prompt handling
- provider normalization
- plan building
- ISO resolution
- execution
- success reporting

That made behavior changes risky and pushed coverage effort into broad end-to-end tests instead of narrow helper tests.

**Fix**:
- Extracted deployment plan/build helpers for Proxmox and vSphere
- Extracted dry-run summary builders for all providers
- Split TrueNAS ISO resolution, access setup, manager connection, deploy execution, and success reporting into helpers
- Switched TrueNAS deploy to the existing VM-manager seam and spinner seam
- Added targeted tests around the new helpers instead of only outer command flows

**Status**: ✅ Improved

---

## Testing

After applying fixes, run:

```bash
cd cmd/homeops-cli
make check  # Run all checks (fmt, vet, lint, test)
make build  # Build the binary
```

---

## Future Improvements

1. **Standardize Provider Success Reporting**: TrueNAS, Proxmox, and vSphere are much closer structurally now, but final user-facing deploy summaries still differ in tone and detail.

2. **Reduce Remaining CLI Coupling In `cmd/talos`**: `talosctl`, SSH, and provider clients still dominate the uncovered surface. More seams around live command execution would keep future changes cheaper to test.

3. **Unify Cancellation Semantics**: Some interactive flows still return `nil` on cancellation while others return a cancellation-style error. That inconsistency should be flattened.

4. **Migrate More Call Sites To Shared Logger Patterns**: The global/singleton logger is in place, but most command code still constructs local loggers repeatedly.
