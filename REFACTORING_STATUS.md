# Taskfile to Go Refactoring Status

This document outlines the progress of refactoring the project's Taskfile tasks into a new Go-based CLI tool.

## Overall Status

The refactoring is well underway but not yet complete. The most critical and complex parts of the infrastructure management have been successfully ported to the new Go CLI. The remaining work is primarily focused on Talos VM management and workstation setup.

## Command-by-Command Status

| Command | Status | Notes |
|---|---|---|
| `bootstrap` | ✅ Complete | The `bootstrap-cluster.sh` script has been fully refactored into the `homeops bootstrap` command. It includes features like prerequisite validation, 1Password authentication, Talos configuration, cluster bootstrapping, kubeconfig fetching, node readiness checks, CRD/resource application, and Helmfile syncing. |
| `kubernetes` | ✅ Complete | All tasks from the `kubernetes` Taskfile have been refactored into the `homeops k8s` command. The Go implementation is more robust, with features like automatic installation of `krew` plugins, dry-run modes, and better logging. |
| `talos` | 🟡 In Progress | The `talos` commands are partially refactored. |
| | ✅ Node/Cluster Management | Tasks for managing Talos nodes and the cluster (`apply-node`, `upgrade-node`, `upgrade-k8s`, `reboot-node`, `shutdown-cluster`, `reset-node`, `reset-cluster`, `kubeconfig`) are fully implemented in Go. |
| | ❌ VM Management | Tasks for managing TrueNAS VMs (`deploy-vm`, `list-vms`, `start-vm`, `stop-vm`, `info-vm`, `delete-vm`) are currently thin wrappers that call the old Taskfile tasks. The underlying shell scripts have not yet been refactored into Go. |
| `volsync` | 🟡 Almost Complete | The `volsync` tasks have been almost completely refactored into the `homeops volsync` command. The core logic is ported, but the Go implementation uses hardcoded YAML templates for the `ReplicationDestination` and unlock job resources instead of the original `.j2` templates. This is a reasonable simplification for the CLI, but it's a deviation from the original implementation. |
| `workstation` | ❌ Not Started | The `workstation` tasks for setting up Homebrew and Krew have not been refactored into the Go CLI. The `main.go` file has a placeholder for the command, but the implementation is missing. |

## Next Steps

The following items need to be addressed to complete the refactoring:

1.  **Complete the `talos` refactoring:**
    *   Refactor the `deploy-truenas-vm` and `manage-truenas-vm` scripts into Go.
    *   Update the `homeops talos` commands to use the new Go-based VM management functions.
2.  **Complete the `workstation` refactoring:**
    *   Implement the `homeops workstation brew` command to install Homebrew packages from the `Brewfile`.
    *   Implement the `homeops workstation krew` command to install the required `krew` plugins.
3.  **Review and refine the `volsync` implementation:**
    *   Decide if the hardcoded YAML templates are acceptable for the long term or if a more flexible templating solution is needed.
4.  **Remove the old Taskfiles and scripts:**
    *   Once all tasks have been refactored, the `.taskfiles` directory and the shell scripts in the `scripts` directory can be removed.
