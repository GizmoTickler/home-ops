# vSphere/ESXi VM Deployment Guide

This guide covers deploying Talos VMs on vSphere/ESXi using the homeops-cli tool.

## Prerequisites

1. **ESXi Configuration**:
   - ESXi 8 standalone host configured
   - NFS datastore `truenas-nfs` with Talos ISO
   - Network port group `vl999` configured

2. **Environment Variables or 1Password**:
   - Set vSphere credentials:
     ```bash
     export VSPHERE_HOST=<esxi-host-ip>
     export VSPHERE_USERNAME=root
     export VSPHERE_PASSWORD=<password>
     ```
   - Or configure in 1Password:
     - `op://Infrastructure/esxi/host`
     - `op://Infrastructure/esxi/username`
     - `op://Infrastructure/esxi/password`

3. **Talos ISO**:
   - Upload the Talos ISO to the `datastore1` datastore
   - Default location: `[datastore1] vmware-amd64.iso`

## Usage Examples

### Deploy a Single VM

Deploy a single Talos VM with default specifications:

```bash
./homeops-cli talos deploy-vm \
  --provider vsphere \
  --name k8s-01 \
  --memory 49152 \
  --vcpus 16 \
  --disk-size 500 \
  --rook-size 1024
```

### Deploy Multiple VMs in Parallel

Deploy 3 VMs concurrently with automatic numbering:

```bash
./homeops-cli talos deploy-vm \
  --provider vsphere \
  --name k8s \
  --node-count 3 \
  --concurrent 3 \
  --memory 49152 \
  --vcpus 16 \
  --disk-size 500 \
  --rook-size 1024
```

This will create:
- k8s-01
- k8s-02
- k8s-03

### Deploy k8s Cluster Nodes

Deploy the 3 k8s cluster nodes with their predefined MAC addresses:

```bash
./homeops-cli talos deploy-vm \
  --provider vsphere \
  --name k8s \
  --node-count 3 \
  --concurrent 3 \
  --memory 49152 \
  --vcpus 16 \
  --disk-size 500 \
  --rook-size 1024
```

This will create:
- k8s-0 (MAC: 00:a0:98:28:c8:83) - automatically read from node config
- k8s-1 (MAC: 00:a0:98:1a:f3:72) - automatically read from node config
- k8s-2 (MAC: 00:a0:98:3e:6c:22) - automatically read from node config

The MAC addresses are automatically read from the node configuration files in `internal/templates/talos/nodes/`.

### Custom Network and Datastore

Specify custom network and datastore:

```bash
./homeops-cli talos deploy-vm \
  --provider vsphere \
  --name talos-control \
  --datastore truenas-flash \
  --network vl999 \
  --memory 49152 \
  --vcpus 10
```

### Deploy with Custom MAC Address

Deploy with a specific MAC address:

```bash
./homeops-cli talos deploy-vm \
  --provider vsphere \
  --name talos-node-01 \
  --mac-address "00:50:56:01:02:03" \
  --memory 49152 \
  --vcpus 10
```

## Default Specifications

The default VM specifications match your requirements:
- **Memory**: 48GB (49152 MB)
- **vCPUs**: 16
- **Boot Disk**: 500GB (thin provisioned)
- **Rook Disk**: 1024GB / 1TB (thin provisioned)
- **Datastore**: truenas-nfs
- **Network**: vl999
- **ISO**: [datastore1] vmware-amd64.iso
- **Guest OS**: Other 6.x or later Linux (64-bit)
- **Network Adapter**: vmxnet3
- **NVMe Controller**: NVMe Controller per disk
- **SATA Controller**: AHCI (for CD-ROM)
- **Add Watchdog Timer**: Yes
- **Disable UEFI Secure Boot**: Yes
- **Add Precision Clock**: Yes

## Parallel Deployment

The vSphere provider supports parallel VM deployment for faster provisioning:

```bash
# Deploy 6 worker nodes, 3 at a time
./homeops-cli talos deploy-vm \
  --provider vsphere \
  --name talos-worker \
  --node-count 6 \
  --concurrent 3
```

This will deploy:
- First batch: talos-worker-01, talos-worker-02, talos-worker-03
- Second batch: talos-worker-04, talos-worker-05, talos-worker-06

## VM Management

While the deployment command creates VMs on vSphere, you can manage them using standard vSphere tools or the vSphere web client.

## Comparison with TrueNAS Provider

| Feature | TrueNAS | vSphere/ESXi |
|---------|---------|--------------|
| Provider Flag | `--provider truenas` (default) | `--provider vsphere` |
| Storage | ZVols on TrueNAS | VMDK on iSCSI datastore |
| Parallel Deploy | No | Yes (`--concurrent`, `--node-count`) |
| Console | SPICE | vSphere Console |
| Network | Bridge (br0) | Port Group (vl999) |

## Troubleshooting

1. **Connection Failed**: Verify ESXi host is reachable and credentials are correct
2. **Datastore Not Found**: Ensure `truenas-nfs` NFS datastore is mounted
3. **ISO Not Found**: Check that the ISO exists at `[datastore1] vmware-amd64.iso`
4. **Network Not Found**: Verify port group `vl999` exists in ESXi networking

## Notes

- All disks are thin provisioned to optimize storage usage
- VMs are automatically powered on after creation
- The ISO must be present on the NFS datastore before deployment
- For custom ISOs, run `homeops talos prepare-iso` first to generate and upload the ISO