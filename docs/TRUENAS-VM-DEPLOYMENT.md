# TrueNAS Scale VM Deployment for Talos

This document describes how to deploy Talos VMs to TrueNAS Scale 25.04.2+ using the new JSON-RPC 2.0 WebSocket API.

## Prerequisites

1. **TrueNAS Scale 25.04.2+** running with virtualization enabled
2. **Go 1.21+** for building the tools (or use pre-built binaries)
3. **Network bridge** configured on TrueNAS (typically `br0`)
4. **Storage pool** available for VM disks (typically `tank`)
5. **TrueNAS API Key** with administrative privileges

## Building the Tools

The VM deployment tools are written in Go. To build them:

```bash
cd scripts
make all
```

This will create two binaries:
- `deploy-truenas-vm` - Deploy new VMs
- `manage-truenas-vm` - Manage existing VMs

## Setting up 1Password Integration

The tasks use 1Password to securely store and retrieve TrueNAS credentials. You need to set up the following items in your 1Password vault:

### 1Password Setup

1. Create a new item in 1Password under the **Infrastructure** vault
2. Name it **talosdeploy**
3. Add the following fields:
   - `TRUENAS_HOST` - Your TrueNAS hostname or IP address
   - `TRUENAS_API` - Your TrueNAS API key
   - `TRUENAS_SPICE_PASS` - SPICE password for remote access (optional)

### Creating a TrueNAS API Key

1. Log into your TrueNAS Scale web interface
2. Go to **System Settings** → **API Keys**
3. Click **Add** to create a new API key
4. Give it a descriptive name (e.g., "VM Management")
5. Copy the generated API key and store it in 1Password as `TRUENAS_API`

## Available Tasks

### Deploy a New VM

### Deploy with Pre-created ZVols (Recommended)

For production deployments with thin-provisioned ZVols that you've already created:

```bash
task talos:deploy-vm-with-pattern NAME=k8s-master-01 \
  POOL=tank \
  MEMORY=8192 \
  VCPUS=4 \
  DISK_SIZE=50 \
  MAC_ADDRESS=52:54:00:12:34:56 \
  USE_SPICE=true
```

This uses a standardized naming pattern:
- Boot disk: `tank/vms/k8s-master-01-boot`
- OpenEBS disk: `tank/vms/k8s-master-01-openebs`
- Rook disk: `tank/vms/k8s-master-01-rook`

### Deploy with Custom ZVol Paths

For custom ZVol locations:

```bash
task talos:deploy-vm NAME=talos-node-1 \
  MEMORY=8192 \
  VCPUS=4 \
  DISK_SIZE=50 \
  MAC_ADDRESS=52:54:00:12:34:57 \
  BOOT_ZVOL=mypool/kubernetes/node1-boot \
  OPENEBS_ZVOL=mypool/kubernetes/node1-openebs \
  ROOK_ZVOL=mypool/kubernetes/node1-rook \
  SKIP_ZVOL_CREATE=true
```

### Deploy with Auto-created ZVols

For development or testing (creates ZVols automatically):

```bash
task talos:deploy-vm NAME=test-node-1 \
  MEMORY=4096 \
  VCPUS=2 \
  DISK_SIZE=20
```

**Parameters:**
- `NAME` (required): VM name
- `MEMORY` (optional): Memory in MB (default: 4096)
- `VCPUS` (optional): Number of vCPUs (default: 2)
- `DISK_SIZE` (optional): Boot disk size in GB (default: 20)
- `MAC_ADDRESS` (optional): Specific MAC address for the VM
- `BOOT_ZVOL` (optional): Boot disk ZVol path
- `OPENEBS_ZVOL` (optional): OpenEBS storage ZVol path
- `ROOK_ZVOL` (optional): Rook storage ZVol path
- `SKIP_ZVOL_CREATE` (optional): Skip ZVol creation (default: false)
- `USE_SPICE` (optional): Use SPICE display instead of VNC (default: false)
- `TALOS_ISO` (optional): Talos ISO URL (default: latest release)
- `NETWORK_BRIDGE` (optional): Network bridge name (default: br0)

**Note:** TrueNAS host, API key, and SPICE password are automatically retrieved from 1Password using `op inject`.

### List VMs

List all virtual machines on TrueNAS:

```bash
task talos:list-vms
```

### Start a VM

Start a specific virtual machine:

```bash
task talos:start-vm NAME=talos-node-1
```

### Stop a VM

Stop a specific virtual machine:

```bash
task talos:stop-vm NAME=talos-node-1
```

### Delete a VM

Delete a virtual machine (with confirmation prompt):

```bash
task talos:delete-vm NAME=talos-node-1
```

## VM Configuration Details

### Default VM Specifications

- **Bootloader**: UEFI
- **Time**: LOCAL
- **Shutdown Timeout**: 90 seconds
- **Autostart**: Disabled (can be enabled via TrueNAS UI)

### Storage Configuration

- **ZVol Path**: `/dev/zvol/tank/vms/{VM_NAME}`
- **Disk Type**: VirtIO (for optimal performance)
- **Block Size**: 16K (optimized for VM workloads)

### Network Configuration

- **NIC Type**: VirtIO (for optimal performance)
- **MAC Address**: Auto-generated
- **Bridge**: Configurable (default: br0)

### Display Configuration

**VNC (Default)**:
- **Type**: VNC
- **Bind Address**: 0.0.0.0 (accessible from any IP)
- **Port**: Auto-assigned
- **Web Access**: Enabled
- **Password**: Auto-generated (8 characters)

**SPICE (Optional)**:
- **Type**: SPICE
- **Bind Address**: 0.0.0.0 (accessible from any IP)
- **Password**: Retrieved from 1Password (`TRUENAS_SPICE_PASS`)
- **Benefits**: Better performance, clipboard sharing, USB redirection

## TrueNAS Scale 25.04.2 API Features

This implementation leverages the new features in TrueNAS Scale 25.04.2:

### JSON-RPC 2.0 WebSocket API

- **Versioned API**: Uses `/api/v25.04.2/websocket` endpoint
- **Real-time Communication**: WebSocket-based for efficient communication
- **Authentication**: Session-based authentication with login tokens
- **Error Handling**: Comprehensive error reporting and handling

### Virtual Machine Management

- **VM Creation**: `vm.create` method with comprehensive device configuration
- **VM Control**: `vm.start`, `vm.stop`, `vm.poweroff` methods
- **VM Querying**: `vm.query` method with filtering capabilities
- **VM Deletion**: `vm.delete` method with cleanup options

### Storage Management

- **Dataset Creation**: `pool.dataset.create` for ZVol creation
- **Dataset Querying**: `pool.dataset.query` for existing storage check
- **Dataset Deletion**: `pool.dataset.delete` with recursive options

## Security Considerations

### Authentication

- Uses TrueNAS API key authentication (Bearer token)
- No session management required - stateless authentication
- SSL/TLS encryption by default (can be disabled for testing)
- Credentials securely managed through 1Password integration
- Uses `op inject` to retrieve secrets at runtime without exposing them

### Network Security

- WebSocket connections use WSS (WebSocket Secure) by default
- Configurable port (default: 443)
- Certificate validation (can be customized)

### VM Security

- VNC access with auto-generated passwords
- Isolated VM networking through bridge configuration
- UEFI secure boot support (configurable)

## Troubleshooting

### Common Issues

1. **Connection Failed**
   - Verify TrueNAS hostname/IP is correct
   - Check if TrueNAS is running and accessible
   - Ensure port 443 (or custom port) is open

2. **Authentication Failed**
   - Verify API key is correct and not expired
   - Check if API key has administrative privileges
   - Ensure TrueNAS API is enabled

3. **VM Creation Failed**
   - Verify storage pool exists and has sufficient space
   - Check if network bridge is configured
   - Ensure VM name is unique

4. **ZVol Creation Failed**
   - Verify storage pool permissions
   - Check available disk space
   - Ensure parent dataset exists

### Debug Mode

Enable debug logging by modifying the scripts:

```python
logging.basicConfig(level=logging.DEBUG)
```

### Manual API Testing

Test API connectivity manually:

```bash
# Test WebSocket connection
python3 -c "
import asyncio
import websockets
import json

async def test():
    uri = 'wss://your-truenas-host/api/v25.04.2/websocket'
    async with websockets.connect(uri) as ws:
        auth = {'id': 1, 'method': 'auth.login', 'params': ['root', 'password']}
        await ws.send(json.dumps(auth))
        response = await ws.recv()
        print(response)

asyncio.run(test())
"
```

## Integration with Existing Workflow

### Talos Cluster Deployment

1. **Deploy VMs**: Use `talos:deploy-vm` to create multiple VMs
2. **Start VMs**: Use `talos:start-vm` to boot the VMs
3. **Configure Talos**: Use existing `talos:apply-node` tasks
4. **Bootstrap Cluster**: Use existing `bootstrap:default` task

## ZVol Naming Pattern

Use the helper task to see the ZVol naming pattern:

```bash
task talos:show-zvol-pattern NAME=k8s-master-01 POOL=tank
```

Output:
```
ZVol naming pattern for VM: k8s-master-01
Pool: tank

Boot disk:    tank/vms/k8s-master-01-boot
OpenEBS disk: tank/vms/k8s-master-01-openebs
Rook disk:    tank/vms/k8s-master-01-rook

Device paths:
Boot disk:    /dev/zvol/tank/vms/k8s-master-01-boot
OpenEBS disk: /dev/zvol/tank/vms/k8s-master-01-openebs
Rook disk:    /dev/zvol/tank/vms/k8s-master-01-rook
```

## Creating ZVols with Thin Provisioning

Before deploying VMs, create your ZVols with thin provisioning enabled:

```bash
# Create parent dataset
zfs create tank/vms

# Create thin-provisioned ZVols
zfs create -V 50G -s tank/vms/k8s-master-01-boot      # Boot disk
zfs create -V 100G -s tank/vms/k8s-master-01-openebs # OpenEBS storage
zfs create -V 200G -s tank/vms/k8s-master-01-rook    # Rook storage
```

The `-s` flag enables sparse (thin) provisioning.

### Example Multi-Node Deployment

```bash
# Deploy control plane nodes with pre-created ZVols and SPICE
for i in {1..3}; do
  task talos:deploy-vm-with-pattern NAME=k8s-master-0$i \
    MEMORY=8192 VCPUS=4 DISK_SIZE=50 \
    MAC_ADDRESS=52:54:00:12:34:$(printf "%02d" $((50+$i))) \
    USE_SPICE=true
done

# Deploy worker nodes with pre-created ZVols and SPICE
for i in {1..3}; do
  task talos:deploy-vm-with-pattern NAME=k8s-worker-0$i \
    MEMORY=16384 VCPUS=8 DISK_SIZE=50 \
    MAC_ADDRESS=52:54:00:12:34:$(printf "%02d" $((60+$i))) \
    USE_SPICE=true
done

# Start all VMs
for i in {1..3}; do
  task talos:start-vm NAME=k8s-master-0$i
  task talos:start-vm NAME=k8s-worker-0$i
done
```

## Future Enhancements

- **ISO Upload**: Automatic Talos ISO upload to TrueNAS
- **Template Support**: VM template creation and cloning
- **Bulk Operations**: Deploy multiple VMs with single command
- **Monitoring Integration**: VM status monitoring and alerting
- **Backup Integration**: Automated VM snapshot and backup
- **GPU Passthrough**: Support for GPU device assignment
- **Advanced Networking**: VLAN and advanced network configuration

## References

- [TrueNAS Scale 25.04 Release Notes](https://www.truenas.com/docs/scale/25.04/gettingstarted/scalereleasenotes/)
- [TrueNAS API Documentation](https://api.truenas.com/v25.04.2/)
- [Talos Linux Documentation](https://www.talos.dev/)
- [WebSocket API Specification](https://tools.ietf.org/html/rfc6455)
- [JSON-RPC 2.0 Specification](https://www.jsonrpc.org/specification)
