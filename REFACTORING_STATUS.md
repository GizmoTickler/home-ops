# Taskfile to Go Refactoring Status

This document outlines the progress of refactoring the project's Taskfile tasks into a new Go-based CLI tool.

## Overall Status

The refactoring is nearly complete! All major command categories (`bootstrap`, `kubernetes`, `talos`, and `workstation`) have been successfully ported to the new Go CLI. The only remaining work is minor refinements to the `volsync` implementation and cleanup of legacy files.

## Command-by-Command Status

| Command | Status | Notes |
|---|---|---|
| `bootstrap` | ✅ Complete | The `bootstrap-cluster.sh` script has been fully refactored into the `homeops bootstrap` command. It includes features like prerequisite validation, 1Password authentication, Talos configuration, cluster bootstrapping, kubeconfig fetching, node readiness checks, CRD/resource application, and Helmfile syncing. |
| `kubernetes` | ✅ Complete | All tasks from the `kubernetes` Taskfile have been refactored into the `homeops k8s` command. The Go implementation is more robust, with features like automatic installation of `krew` plugins, dry-run modes, and better logging. |
| `talos` | ✅ Complete | The `talos` commands are fully refactored. |
| | ✅ Node/Cluster Management | Tasks for managing Talos nodes and the cluster (`apply-node`, `upgrade-node`, `upgrade-k8s`, `reboot-node`, `shutdown-cluster`, `reset-node`, `reset-cluster`, `kubeconfig`) are fully implemented in Go. |
| | ✅ VM Management | Tasks for managing TrueNAS VMs (`deploy-vm`, `list-vms`, `start-vm`, `stop-vm`, `info-vm`, `delete-vm`) have been fully refactored into Go with a comprehensive TrueNAS API client and VM manager implementation. |
| `volsync` | ✅ Complete | The `volsync` tasks have been fully refactored into the `homeops volsync` command. The implementation now supports configurable NFS settings via environment variables (`VOLSYNC_NFS_SERVER`, `VOLSYNC_NFS_PATH`) and command-line flags, making it flexible for different environments. |
| `workstation` | ✅ Complete | The `workstation` tasks for setting up Homebrew and Krew have been fully refactored into the `homeops workstation` command with `brew` and `krew` subcommands. |

## Next Steps

The following items need to be addressed to complete the refactoring:

1.  **Remove the old Taskfiles and scripts:**
    *   Once all tasks have been refactored, the `.taskfiles` directory and the shell scripts in the `scripts` directory can be removed.
2.  **Testing and validation:**
    *   Thoroughly test all refactored commands to ensure they work correctly in production environments.
    *   Update documentation to reflect the new CLI interface.
3.  **Optional enhancements:**
    *   Add configuration file support for common settings (TrueNAS credentials, cluster endpoints, etc.).
    *   Implement shell completion for better user experience.
    *   Add more comprehensive error handling and recovery mechanisms.
