# HomeOps CLI Commands Documentation

## Overview

The HomeOps CLI is a comprehensive tool for managing Talos Linux clusters on vSphere/ESXi and TrueNAS infrastructure. It handles everything from custom ISO generation to VM deployment and cluster management.

## Command Structure

```bash
homeops-cli talos <command> [subcommand] [flags]
```

## Core Commands

### 1. `prepare-iso` - Custom ISO Generation and Upload

**Purpose**: Generates a custom Talos ISO using schematic configuration and uploads it to storage providers.

```bash
homeops-cli talos prepare-iso [flags]
```

**What it does**:
1. Loads schematic configuration from `internal/templates/talos/schematic.yaml`
2. Generates a new schematic ID via Talos factory API
3. Creates a custom ISO with the schematic
4. Updates `controlplane.yaml` template with the new schematic ID
5. Uploads ISO to specified provider (TrueNAS or vSphere)

**Flags**:
- `--provider string`: Storage provider (`truenas` or `vsphere`) (default: `truenas`)

**Examples**:
```bash
# Generate and upload ISO to TrueNAS
homeops-cli talos prepare-iso

# Generate and upload ISO to vSphere datastore
homeops-cli talos prepare-iso --provider vsphere
```

**Output Files**:
- ISO uploaded to storage (e.g., `vmware-amd64.iso` for vSphere)
- Updated `internal/templates/talos/controlplane.yaml` with new schematic ID
- Cached ISO info in `.cache/talos-isos/`

---

### 2. `deploy-vm` - Virtual Machine Deployment

**Purpose**: Deploys one or more Talos VMs on TrueNAS or vSphere/ESXi with proper configuration.

```bash
homeops-cli talos deploy-vm [flags]
```

**What it does**:
1. Connects to virtualization platform (TrueNAS or vSphere)
2. Creates VMs with specified configuration
3. Configures SR-IOV networking with MAC addresses from templates
4. Sets up storage disks (boot, OpenEBS, Rook)
5. Connects prepared ISO for installation
6. Configures memory reservation and MTU settings

**Flags**:
- `--provider string`: Virtualization provider (`truenas` or `vsphere`) (default: `truenas`)
- `--name string`: VM name (required for single VM, base name for multiple VMs)
- `--node-count int`: Number of VMs to deploy (vSphere only) (default: `1`)
- `--concurrent int`: Number of concurrent VM deployments (vSphere only) (default: `3`)
- `--memory int`: Memory in MB (default: `49152` = 48GB)
- `--vcpus int`: Number of vCPUs (default: `10`)
- `--disk-size int`: Boot disk size in GB (default: `250`)
- `--openebs-size int`: OpenEBS disk size in GB (default: `1024`)
- `--rook-size int`: Rook disk size in GB (default: `800`)
- `--datastore string`: Datastore name (vSphere only) (default: `truenas-flash`)
- `--network string`: Network port group name (vSphere only) (default: `vl999`)
- `--pool string`: Storage pool (TrueNAS only) (default: `flashstor/VM`)
- `--generate-iso`: Generate custom ISO using schematic.yaml
- `--mac-address string`: MAC address (optional)
- `--skip-zvol-create`: Skip ZVol creation (TrueNAS only)

**Examples**:
```bash
# Deploy single VM on vSphere
homeops-cli talos deploy-vm --provider vsphere --name test-node

# Deploy 3 k8s nodes concurrently on vSphere (creates k8s-0, k8s-1, k8s-2)
homeops-cli talos deploy-vm --provider vsphere --name k8s --node-count 3 --concurrent 3

# Deploy VM with custom specifications
homeops-cli talos deploy-vm --provider vsphere --name worker --memory 32768 --vcpus 16 --disk-size 500

# Deploy VM and generate custom ISO
homeops-cli talos deploy-vm --provider vsphere --name k8s --node-count 3 --generate-iso
```

**VM Configuration Applied**:
- **Memory**: Full reservation for SR-IOV (default 48GB)
- **Network**: SR-IOV with manual MAC addresses from node configs
- **MTU**: Guest OS MTU change allowed = true
- **Storage**: 3 disks (boot, OpenEBS, Rook) with proper sizing
- **ISO**: Connected with prepared Talos ISO

---

### 3. `manage-vm` - VM Lifecycle Management

**Purpose**: Manages VM lifecycle operations (list, start, stop, delete, etc.).

#### 3.1 `manage-vm list`
```bash
homeops-cli talos manage-vm list
```
Lists all VMs on TrueNAS.

#### 3.2 `manage-vm delete`
```bash
homeops-cli talos manage-vm delete [flags]
```

**Flags**:
- `--provider string`: Virtualization provider (`truenas` or `vsphere`) (default: `truenas`)
- `--name string`: VM name (required)
- `--force`: Force deletion without confirmation

**Examples**:
```bash
# Delete VM on vSphere
homeops-cli talos manage-vm delete --provider vsphere --name k8s-0 --force

# Delete VM on TrueNAS
homeops-cli talos manage-vm delete --name test-vm
```

#### 3.3 `manage-vm poweroff/poweron`
```bash
homeops-cli talos manage-vm poweroff --provider vsphere --name k8s-0
homeops-cli talos manage-vm poweron --provider vsphere --name k8s-0
```

#### 3.4 `manage-vm cleanup-zvols`
```bash
homeops-cli talos manage-vm cleanup-zvols --name vm-name
```
Cleans up orphaned ZVols for deleted TrueNAS VMs.

---

### 4. Node Operations

#### 4.1 `apply-node` - Apply Talos Configuration
```bash
homeops-cli talos apply-node --ip <node-ip>
```
Applies Talos configuration to a specific node.

#### 4.2 `reboot-node` - Reboot Node
```bash
homeops-cli talos reboot-node --ip <node-ip> [--mode <mode>]
```

**Flags**:
- `--ip string`: Node IP address (required)
- `--mode string`: Reboot mode (default: `powercycle`)

#### 4.3 `reset-node` - Reset Node
```bash
homeops-cli talos reset-node --ip <node-ip>
```

#### 4.4 `upgrade-node` - Upgrade Talos
```bash
homeops-cli talos upgrade-node --ip <node-ip>
```

---

### 5. Cluster Operations

#### 5.1 `upgrade-k8s` - Kubernetes Upgrade
```bash
homeops-cli talos upgrade-k8s
```
Upgrades Kubernetes across the entire cluster.

#### 5.2 `reset-cluster` - Cluster Reset
```bash
homeops-cli talos reset-cluster
```
Resets Talos across the whole cluster.

#### 5.3 `shutdown-cluster` - Cluster Shutdown
```bash
homeops-cli talos shutdown-cluster
```
Shuts down Talos across the whole cluster.

#### 5.4 `kubeconfig` - Generate Kubeconfig
```bash
homeops-cli talos kubeconfig
```
Generates the kubeconfig for the Talos cluster.

---

## Typical Workflows

### Complete Fresh Deployment

1. **Prepare custom ISO**:
   ```bash
   homeops-cli talos prepare-iso --provider vsphere
   ```

2. **Deploy cluster nodes**:
   ```bash
   homeops-cli talos deploy-vm --provider vsphere --name k8s --node-count 3 --concurrent 3
   ```

3. **Apply configurations** (after VMs are powered on):
   ```bash
   homeops-cli talos apply-node --ip 192.168.122.10
   homeops-cli talos apply-node --ip 192.168.122.11
   homeops-cli talos apply-node --ip 192.168.122.12
   ```

4. **Generate kubeconfig**:
   ```bash
   homeops-cli talos kubeconfig
   ```

### Update Existing Cluster

1. **Update schematic and prepare new ISO**:
   ```bash
   homeops-cli talos prepare-iso --provider vsphere
   ```

2. **Upgrade nodes** (one by one):
   ```bash
   homeops-cli talos upgrade-node --ip 192.168.122.10
   homeops-cli talos upgrade-node --ip 192.168.122.11
   homeops-cli talos upgrade-node --ip 192.168.122.12
   ```

### Replace/Rebuild Nodes

1. **Delete old VMs**:
   ```bash
   homeops-cli talos manage-vm delete --provider vsphere --name k8s-0 --force
   homeops-cli talos manage-vm delete --provider vsphere --name k8s-1 --force
   homeops-cli talos manage-vm delete --provider vsphere --name k8s-2 --force
   ```

2. **Deploy new VMs**:
   ```bash
   homeops-cli talos deploy-vm --provider vsphere --name k8s --node-count 3 --concurrent 3
   ```

## Configuration Files

### Node Templates
- `internal/templates/talos/nodes/192.168.122.10.yaml` - k8s-0 config
- `internal/templates/talos/nodes/192.168.122.11.yaml` - k8s-1 config  
- `internal/templates/talos/nodes/192.168.122.12.yaml` - k8s-2 config

### Schematic Configuration
- `internal/templates/talos/schematic.yaml` - Custom Talos build configuration

### Main Templates
- `internal/templates/talos/controlplane.yaml` - Control plane configuration (auto-updated with schematic ID)

## Environment Variables

The CLI uses 1Password for credential management. Required secrets:
- vSphere credentials stored in 1Password Infrastructure vault
- Talos certificates and tokens in 1Password Infrastructure vault

## Cache and Artifacts

- `.cache/talos-isos/` - Cached ISO information
- `kubeconfig` - Generated cluster kubeconfig (after `talos kubeconfig`)
- `talosconfig` - Talos configuration for cluster access

## Tips and Best Practices

1. **Always use `prepare-iso` before deploying new VMs** to ensure latest schematic
2. **Use consistent naming**: `k8s` base name creates `k8s-0`, `k8s-1`, `k8s-2`
3. **Leverage concurrent deployment**: Use `--concurrent 3` for faster multi-VM deployment
4. **Check VM status** with govc or vSphere client after deployment
5. **Power on VMs manually** after deployment before applying configurations
6. **Use `--force` flag** for non-interactive VM deletion in scripts

## Command Quick Reference

| Command | Purpose | Example |
|---------|---------|---------|
| `prepare-iso` | Generate custom ISO | `homeops-cli talos prepare-iso --provider vsphere` |
| `deploy-vm` | Deploy VMs | `homeops-cli talos deploy-vm --provider vsphere --name k8s --node-count 3` |
| `manage-vm delete` | Delete VM | `homeops-cli talos manage-vm delete --provider vsphere --name k8s-0 --force` |
| `apply-node` | Apply config to node | `homeops-cli talos apply-node --ip 192.168.122.10` |
| `upgrade-node` | Upgrade Talos | `homeops-cli talos upgrade-node --ip 192.168.122.10` |
| `upgrade-k8s` | Upgrade Kubernetes | `homeops-cli talos upgrade-k8s` |
| `kubeconfig` | Generate kubeconfig | `homeops-cli talos kubeconfig` |

This documentation covers all major commands and workflows for managing your Talos cluster infrastructure with the HomeOps CLI.