# TrueNAS VM Tools - Quick Reference

## 🚀 Common Commands

### Deploy New VM
```bash
# Standard Kubernetes worker node
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

### VM Management
```bash
# List all VMs
./manage-truenas-vm -action list \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Get VM details
./manage-truenas-vm -action info -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Start VM
./manage-truenas-vm -action start -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Stop VM
./manage-truenas-vm -action stop -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"

# Delete VM and all ZVols
./manage-truenas-vm -action delete -name "k8s-worker-01" \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"
```

## 📊 VM Size Presets

### Control Plane Node
```bash
-memory 8192 -vcpus 4 -disk-size 250 -openebs-size 1024 -rook-size 800
```

### Worker Node (Small)
```bash
-memory 8192 -vcpus 4 -disk-size 250 -openebs-size 512 -rook-size 400
```

### Worker Node (Medium)
```bash
-memory 16384 -vcpus 8 -disk-size 250 -openebs-size 1024 -rook-size 800
```

### Worker Node (Large)
```bash
-memory 32768 -vcpus 16 -disk-size 250 -openebs-size 2048 -rook-size 1600
```

## 🔧 Troubleshooting Commands

### Check VM Status
```bash
# Quick status check
./manage-truenas-vm -action list | grep "vm-name"

# Detailed information
./manage-truenas-vm -action info -name "vm-name"
```

### Debug ZVol Issues
```bash
# Check if ZVols exist (run on TrueNAS)
zfs list -t volume | grep vm-name

# Check ZVol usage
zfs get used,available,referenced pool/VM/vm-name-boot
```

### Network Debugging
```bash
# Check MAC address conflicts
./manage-truenas-vm -action list | grep -A1 -B1 "00:30:93:12:ae:XX"

# Verify bridge configuration (run on TrueNAS)
ip link show br0
```

## 🎯 Best Practices

### Naming Conventions
- **VM Names**: `k8s-{role}-{number}` (e.g., `k8s-worker-01`)
- **ZVol Paths**: `{pool}/VM/{vm-name}-{type}` (e.g., `flashstor/VM/k8s-worker-01-boot`)
- **MAC Addresses**: `00:30:93:12:ae:{XX}` (sequential)

### Resource Allocation
- **Boot Disk**: 250GB default (OS + container images + local storage)
- **OpenEBS Disk**: 1TB default (persistent volumes)
- **Rook Disk**: 800GB default (Ceph storage)
- **Memory**: 8-32GB (based on workload)
- **vCPUs**: 4-16 (based on workload)

### Security
- Always use 1Password for credentials
- Use unique MAC addresses
- Secure SPICE passwords
- Regular security updates

## 🔄 Batch Operations

### Deploy Multiple VMs
```bash
#!/bin/bash
for i in {01..03}; do
  ./deploy-truenas-vm \
    -name "k8s-worker-${i}" \
    -boot-zvol "flashstor/VM/k8s-worker-${i}-boot" \
    -openebs-zvol "flashstor/VM/k8s-worker-${i}-ebs" \
    -rook-zvol "flashstor/VM/k8s-worker-${i}-rook" \
    -mac-address "00:30:93:12:ae:2${i}" \
    -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
    -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API" \
    -spice-password "op://Infrastructure/talosdeploy/TRUENAS_SPICE_PASS"
done
```

### Start All VMs
```bash
#!/bin/bash
for vm in k8s-control-01 k8s-worker-01 k8s-worker-02 k8s-worker-03; do
  ./manage-truenas-vm -action start -name "$vm" \
    -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
    -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"
done
```

### Stop All VMs
```bash
#!/bin/bash
for vm in k8s-control-01 k8s-worker-01 k8s-worker-02 k8s-worker-03; do
  ./manage-truenas-vm -action stop -name "$vm" \
    -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
    -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"
done
```

## 📋 Checklists

### Pre-Deployment Checklist
- [ ] 1Password CLI configured and authenticated
- [ ] TrueNAS API key has VM management permissions
- [ ] Storage pool has sufficient space
- [ ] Network bridge is configured
- [ ] MAC addresses are unique
- [ ] ZVol paths don't conflict

### Post-Deployment Checklist
- [ ] VM appears in TrueNAS web interface
- [ ] All devices are present (CDROM, NIC, disks, display)
- [ ] VM can be started successfully
- [ ] SPICE display is accessible
- [ ] Network connectivity works
- [ ] Storage devices are recognized

### Pre-Deletion Checklist
- [ ] VM is stopped
- [ ] Important data is backed up
- [ ] VM is not part of active cluster
- [ ] Confirm ZVol deletion is intended

## 🚨 Emergency Procedures

### Force Stop VM
```bash
./manage-truenas-vm -action stop -name "vm-name" -force \
  -truenas-host "op://Infrastructure/talosdeploy/TRUENAS_HOST" \
  -truenas-api-key "op://Infrastructure/talosdeploy/TRUENAS_API"
```

### Emergency VM Recovery
```bash
# If VM is corrupted, restore from snapshot
zfs rollback pool/VM/vm-name-boot@last-good-snapshot

# If ZVol is corrupted, restore from backup
zfs receive pool/VM/vm-name-boot < backup-file.zfs
```

### Clean Up Orphaned Resources
```bash
# Find orphaned ZVols
zfs list -t volume | grep VM | grep -v "$(./manage-truenas-vm -action list | grep -o 'k8s-[^[:space:]]*')"

# Manual ZVol cleanup (DANGEROUS - verify first!)
# zfs destroy pool/VM/orphaned-zvol
```

## 📞 Support

### Log Locations
- **Deployment logs**: Console output during deployment
- **TrueNAS logs**: `/var/log/middlewared.log`
- **VM logs**: TrueNAS web interface → Virtual Machines → VM → Logs

### Common Error Codes
- **Connection refused**: Check TrueNAS host/port
- **Authentication failed**: Verify API key
- **ZVol exists**: Use `-skip-zvol-create` or choose different path
- **Insufficient space**: Check pool capacity
- **Device creation failed**: Check TrueNAS logs for details

### Getting Help
1. Check this quick reference
2. Review full README.md
3. Check TrueNAS logs
4. Verify 1Password configuration
5. Test with minimal configuration
