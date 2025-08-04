# TrueNAS VM Automation Tools

Enterprise-grade automation tools for deploying and managing virtual machines on TrueNAS SCALE with complete lifecycle management.

## 🚀 Features

### ✅ Complete VM Deployment
- **Automatic thin provisioned ZVol creation** with configurable sizes
- **Full device support**: CDROM, NIC, multiple disks, SPICE display
- **1Password integration** for secure credential management
- **Talos Linux optimized** with proper UEFI and CPU configuration

### ✅ Complete VM Management
- **Smart ZVol discovery and cleanup** - no orphaned storage
- **VM lifecycle operations**: start, stop, delete, info, list
- **Real-time status monitoring** and detailed device information
- **Production-ready error handling** and logging

### ✅ Enterprise Features
- **Thin provisioning** for efficient storage utilization
- **Configurable disk sizes** per workload type (boot, OpenEBS, Rook)
- **Custom MAC addresses** for network management
- **Auto-assigned display ports** with SPICE protocol
- **Comprehensive logging** for operations and troubleshooting

## 📦 Tools Overview

| Tool | Purpose | Key Features |
|------|---------|--------------|
| `deploy-truenas-vm` | Deploy new VMs | ZVol creation, device setup, SPICE display |
| `manage-truenas-vm` | Manage existing VMs | Start/stop/delete, info, ZVol cleanup |
| `truenas-deep-explorer` | API exploration | Deep TrueNAS API analysis and debugging |

## 🛠 Installation

### Prerequisites
- Go 1.19+ installed
- 1Password CLI (`op`) configured and authenticated
- Network access to TrueNAS SCALE instance
- TrueNAS API key with VM management permissions

### Build Tools
```bash
# Build all tools
make build

# Build individual tools
make build-deploy    # Deploy tool
make build-manage    # Management tool
make build-explorer  # API explorer
```

## 🎯 Quick Start

### 1. Deploy a New VM
```bash
./deploy-truenas-vm \
  -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API" \
  -boot-zvol "flashstor/VM/k8s-worker-01-boot" \
  -openebs-zvol "flashstor/VM/k8s-worker-01-ebs" \
  -rook-zvol "flashstor/VM/k8s-worker-01-rook" \
  -mac-address "00:30:93:12:ae:20" \
  -spice-password "op://Infrastructure/talosdeploy/TRUENAS_SPICE_PASS" \
  -disk-size 250 \
  -openebs-size 1024 \
  -rook-size 800
```

### 2. Manage Existing VMs
```bash
# List all VMs
./manage-truenas-vm -action list \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Get detailed VM information
./manage-truenas-vm -action info -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Start a VM
./manage-truenas-vm -action start -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Stop a VM
./manage-truenas-vm -action stop -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Delete VM and all associated ZVols
./manage-truenas-vm -action delete -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"
```

## 📋 Configuration Options

### VM Deployment Options

| Flag | Default | Description |
|------|---------|-------------|
| `-name` | *required* | VM name |
| `-memory` | 4096 | Memory in MB |
| `-vcpus` | 2 | Number of vCPUs |
| `-disk-size` | 250 | Boot disk size in GB |
| `-openebs-size` | 1024 | OpenEBS disk size in GB (1TB) |
| `-rook-size` | 800 | Rook disk size in GB |
| `-mac-address` | *auto-generated* | MAC address for VM |
| `-network-bridge` | br0 | Network bridge |
| `-skip-zvol-create` | false | Skip ZVol creation (assume they exist) |

### TrueNAS Connection Options

| Flag | Default | Description |
|------|---------|-------------|
| `-truenas-host` | *required* | TrueNAS hostname or IP |
| `-truenas-api-key` | *required* | TrueNAS API key |
| `-truenas-port` | 443 | TrueNAS port |
| `-no-ssl` | false | Disable SSL/TLS |

### Display Options

| Flag | Default | Description |
|------|---------|-------------|
| `-spice-password` | *required* | SPICE password (from 1Password) |

## 🏗 Architecture

### VM Configuration
- **Bootloader**: UEFI
- **CPU Type**: HOST-PASSTHROUGH (optimal performance)
- **Memory**: Configurable (default 4GB)
- **vCPUs**: Configurable (default 2)
- **Autostart**: Disabled (manual control)

### Storage Layout
- **Boot Disk**: Thin provisioned ZVol for OS (default: 250GB)
- **OpenEBS Disk**: Thin provisioned ZVol for OpenEBS storage (default: 1TB)
- **Rook Disk**: Thin provisioned ZVol for Rook Ceph storage (default: 800GB)
- **All ZVols**: Created with `sparse: true` for space efficiency

### Device Configuration
- **CDROM**: Metal/Talos ISO for installation (order 1000)
- **Boot Disk**: Primary storage device (order 1001)
- **Network**: VIRTIO NIC with custom MAC (order 1002)
- **Display**: SPICE with auto-assigned ports (order 1003)
- **OpenEBS Disk**: Secondary storage (order 1004)
- **Rook Disk**: Tertiary storage (order 1005)

## 🔒 Security

### 1Password Integration
All sensitive credentials are managed through 1Password:
- `TRUENAS_HOST`: TrueNAS server hostname/IP
- `TRUENAS_API`: TrueNAS API key
- `TRUENAS_SPICE_PASS`: SPICE display password

### Network Security
- VMs are isolated on specified bridge network
- SPICE display uses secure authentication
- API communication over HTTPS

## 🐛 Troubleshooting

### Common Issues

**ZVol Creation Fails**
```bash
# Check if parent datasets exist
# Ensure sufficient storage space
# Verify pool permissions
```

**VM Won't Start**
```bash
# Check VM status
./manage-truenas-vm -action info -name "vm-name" ...

# Verify all devices are present
# Check TrueNAS logs for hardware issues
```

**Display Connection Issues**
```bash
# Verify SPICE password is correct
# Check auto-assigned ports in VM info
# Ensure network connectivity to TrueNAS
```

### Debug Mode
Set environment variable for detailed logging:
```bash
export DEBUG=1
./deploy-truenas-vm ...
```

## 📊 Examples

### Kubernetes Control Plane Node
```bash
./deploy-truenas-vm \
  -name "k8s-control-01" \
  -memory 8192 \
  -vcpus 4 \
  -boot-zvol "flashstor/VM/k8s-control-01-boot" \
  -openebs-zvol "flashstor/VM/k8s-control-01-ebs" \
  -rook-zvol "flashstor/VM/k8s-control-01-rook" \
  -mac-address "00:30:93:12:ae:10" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API" \
  -spice-password "op://Infrastructure/talosdeploy/TRUENAS_SPICE_PASS"
```

### Kubernetes Worker Node
```bash
./deploy-truenas-vm \
  -name "k8s-worker-02" \
  -memory 16384 \
  -vcpus 8 \
  -openebs-size 2048 \
  -rook-size 1600 \
  -boot-zvol "flashstor/VM/k8s-worker-02-boot" \
  -openebs-zvol "flashstor/VM/k8s-worker-02-ebs" \
  -rook-zvol "flashstor/VM/k8s-worker-02-rook" \
  -mac-address "00:30:93:12:ae:21" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API" \
  -spice-password "op://Infrastructure/talosdeploy/TRUENAS_SPICE_PASS"
```

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Test thoroughly
5. Submit a pull request

## � Workflow Integration

### CI/CD Pipeline Integration
```bash
# Example GitLab CI job
deploy_vm:
  script:
    - op inject -i deploy-vm.sh | bash
  variables:
    VM_NAME: "k8s-worker-${CI_PIPELINE_ID}"
    BOOT_SIZE: "30"
    OPENEBS_SIZE: "150"
    ROOK_SIZE: "300"
```

### Terraform Integration
```hcl
resource "null_resource" "truenas_vm" {
  provisioner "local-exec" {
    command = <<EOF
      ./deploy-truenas-vm \
        -name "${var.vm_name}" \
        -disk-size ${var.boot_size} \
        -openebs-size ${var.openebs_size} \
        -rook-size ${var.rook_size} \
        -boot-zvol "${var.pool}/VM/${var.vm_name}-boot" \
        -openebs-zvol "${var.pool}/VM/${var.vm_name}-ebs" \
        -rook-zvol "${var.pool}/VM/${var.vm_name}-rook" \
        -mac-address "${var.mac_address}" \
        -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
        -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API" \
        -spice-password "op://Infrastructure/talosdeploy/TRUENAS_SPICE_PASS"
    EOF
    interpreter = ["op", "inject", "-i", "/bin/bash", "-c"]
  }
}
```

## 📈 Performance Considerations

### Storage Performance
- **Thin Provisioning**: Saves space but may impact performance on first write
- **ZVol Block Size**: 16K optimized for VM workloads
- **Pool Layout**: Use mirrored vdevs for better performance

### VM Performance
- **CPU Type**: HOST-PASSTHROUGH provides near-native performance
- **Memory**: Allocate sufficient RAM to avoid swapping
- **Network**: VIRTIO provides optimal network performance

### Scaling Recommendations
- **Control Plane**: 4-8 vCPUs, 8-16GB RAM
- **Worker Nodes**: 8-16 vCPUs, 16-64GB RAM
- **Storage**: Size based on workload requirements

## 🔍 Monitoring and Observability

### VM Metrics
```bash
# Get VM resource usage
./manage-truenas-vm -action info -name "vm-name" | grep -E "(Memory|vCPUs|Status)"

# Monitor all VMs
./manage-truenas-vm -action list | grep -E "(Running|Stopped)"
```

### Storage Metrics
```bash
# Check ZVol usage (run on TrueNAS)
zfs list -t volume | grep VM

# Monitor thin provisioning efficiency
zfs get used,available,referenced pool/VM
```

## 🚨 Disaster Recovery

### Backup Strategy
```bash
# Snapshot all VM ZVols
zfs snapshot -r pool/VM@backup-$(date +%Y%m%d)

# Replicate to backup pool
zfs send pool/VM@backup-20240804 | zfs receive backup-pool/VM
```

### Recovery Procedures
```bash
# Restore from snapshot
zfs rollback pool/VM/vm-name-boot@backup-20240804

# Clone for testing
zfs clone pool/VM/vm-name-boot@backup-20240804 pool/VM/vm-name-boot-test
```

## 📚 API Reference

### TrueNAS API Endpoints Used
- `vm.create` - Create new virtual machine
- `vm.delete` - Delete virtual machine
- `vm.start` - Start virtual machine
- `vm.stop` - Stop virtual machine
- `vm.query` - Query virtual machines
- `vm.device.create` - Create VM device
- `vm.device.query` - Query VM devices
- `pool.dataset.create` - Create ZVol
- `pool.dataset.delete` - Delete ZVol

### Response Formats
All API responses follow JSON-RPC 2.0 format:
```json
{
  "jsonrpc": "2.0",
  "result": [...],
  "id": 1
}
```

## 🎯 Roadmap

### Planned Features
- [ ] **Template Support**: Pre-configured VM templates
- [ ] **Bulk Operations**: Deploy multiple VMs simultaneously
- [ ] **Health Checks**: Automated VM health monitoring
- [ ] **Resource Optimization**: Automatic resource recommendations
- [ ] **Backup Integration**: Automated snapshot management
- [ ] **Metrics Export**: Prometheus metrics endpoint

### Version History
- **v2.0.0**: Enhanced ZVol management and complete device support
- **v1.0.0**: Initial release with basic VM deployment

## �📄 License

This project is part of the home-ops infrastructure automation suite.
