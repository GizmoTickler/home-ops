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

## Testing

After applying fixes, run:

```bash
cd cmd/homeops-cli
make check  # Run all checks (fmt, vet, lint, test)
make build  # Build the binary
```

---

## Future Improvements

1. **Add Test Coverage**: Many internal packages lack tests
   - `internal/ssh`
   - `internal/talos`
   - `internal/templates`
   - `internal/truenas`
   - `internal/ui`
   - `internal/vsphere`
   - `internal/yaml`

2. **Standardize Error Handling**: Some functions return errors on cancellation, others return nil

3. **Remove Deprecated NewClient**: The `vsphere.NewClient()` function has unused parameters

4. **Migrate to Singleton Logger**: Gradually replace `common.NewColorLogger()` with `common.Logger()` calls
