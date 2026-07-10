package proxmox

import (
	"fmt"
	"strings"
	"testing"

	"github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/require"
)

func TestBuildVMOptionsCharacterization(t *testing.T) {
	manager := &VMManager{}
	cases := []struct {
		name     string
		kind     string
		config   VMConfig
		ci       CloudInitConfig
		expected string
	}{
		{
			name: "talos-full-passthrough",
			kind: "talos",
			config: VMConfig{
				Name: "k8s-0", Memory: 8192, Cores: 8, Sockets: 1,
				CPUType: "host", CPUAffinity: "0-7,32-39",
				NUMAEnabled: true, NUMANode: 0,
				BIOS: "ovmf", EFIDiskStorage: "nvme1",
				SCSIController: "virtio-scsi-single",
				BootStorage:    "nvme1", BootDiskSize: 200,
				Discard: true, IOThread: true,
				OpenEBSSize: 100, OpenEBSStorage: "nvmeof-vmdata",
				CephMode: "passthrough", CephDiskByID: "ata-FOO",
				ISOPath:       "local:iso/talos.iso",
				NetworkBridge: "vmbr0", NetworkMTU: 9000, NetworkQueues: 8, VLANID: 999,
				MacAddress:    "00:a0:98:28:c8:83",
				WatchdogModel: "i6300esb", WatchdogAction: "reset",
				AgentEnabled: true, StartOnBoot: true,
			},
			expected: `name=k8s-0
memory=8192
cores=8
sockets=1
ostype=l26
cpu=host
affinity=0-7,32-39
numa=1
numa0=cpus=0-7,hostnodes=0,memory=8192,policy=bind
bios=ovmf
efidisk0=nvme1:1,efitype=4m,pre-enrolled-keys=0
scsihw=virtio-scsi-single
scsi0=nvme1:200,discard=on,iothread=1
scsi1=nvmeof-vmdata:100,discard=on,iothread=1
scsi2=/dev/disk/by-id/ata-FOO,discard=on,iothread=1
ide2=local:iso/talos.iso,media=cdrom
boot=order=ide2
net0=virtio=00:a0:98:28:c8:83,bridge=vmbr0,mtu=9000,queues=8,tag=999
watchdog=model=i6300esb,action=reset
agent=enabled=1
onboot=1
`,
		},
		{
			name: "talos-ceph-virtual",
			kind: "talos",
			config: VMConfig{
				Name: "k8s-1", Memory: 4096, Cores: 4, Sockets: 1,
				BootStorage: "nvme2", BootDiskSize: 150,
				CephMode: "virtual", CephDiskSize: 300,
				NetworkBridge: "vmbr0",
			},
			expected: `name=k8s-1
memory=4096
cores=4
sockets=1
ostype=l26
scsi0=nvme2:150
scsi2=nvme2:300
boot=order=ide2
net0=virtio,bridge=vmbr0
`,
		},
		{
			name: "talos-minimal",
			kind: "talos",
			config: VMConfig{
				Name: "k8s-2", Memory: 2048, Cores: 2, Sockets: 1,
				BootStorage: "local", BootDiskSize: 50,
				NetworkBridge: "vmbr0",
			},
			expected: `name=k8s-2
memory=2048
cores=2
sockets=1
ostype=l26
scsi0=local:50
boot=order=ide2
net0=virtio,bridge=vmbr0
`,
		},
		{
			name: "flatcar-import-ignition",
			kind: "flatcar",
			config: VMConfig{
				Name: "kube-0", Memory: 8192, Cores: 8, Sockets: 1,
				CPUType: "host", CPUAffinity: "0-7",
				NUMAEnabled: true, NUMANode: 1,
				BIOS:           "ovmf",
				SCSIController: "virtio-scsi-single",
				BootStorage:    "nvme1", BootDiskSize: 200,
				Discard: true, IOThread: true,
				ImageDiskPath: "/var/lib/vz/template/flatcar.img",
				OpenEBSSize:   100, OpenEBSStorage: "nvmeof-vmdata",
				CephMode: "virtual", CephDiskSize: 300,
				BootMode:      "order=scsi0;scsi1",
				NetworkBridge: "vmbr0", NetworkMTU: 9000, NetworkQueues: 8, VLANID: 999,
				MacAddress:    "00:a0:98:1a:f3:72",
				WatchdogModel: "i6300esb",
				AgentEnabled:  true, StartOnBoot: true,
				IgnitionPath: "/var/lib/vz/snippets/kube-0.ign",
			},
			expected: `name=kube-0
memory=8192
cores=8
sockets=1
ostype=l26
cpu=host
affinity=0-7
numa=1
numa0=cpus=0-7,hostnodes=1,memory=8192,policy=bind
bios=ovmf
efidisk0=nvme1:1,efitype=4m,pre-enrolled-keys=0
scsihw=virtio-scsi-single
scsi0=nvme1:200,import-from=/var/lib/vz/template/flatcar.img,discard=on,iothread=1
scsi1=nvmeof-vmdata:100,discard=on,iothread=1
scsi2=nvme1:300,discard=on,iothread=1
boot=order=scsi0;scsi1
net0=virtio=00:a0:98:1a:f3:72,bridge=vmbr0,mtu=9000,queues=8,tag=999
watchdog=model=i6300esb
agent=enabled=1
onboot=1
args=-fw_cfg name=opt/org.flatcar-linux/config,file=/var/lib/vz/snippets/kube-0.ign
`,
		},
		{
			name: "flatcar-volume",
			kind: "flatcar",
			config: VMConfig{
				Name: "kube-1", Memory: 4096, Cores: 4, Sockets: 1,
				BootStorage: "nvme2", ImageVolume: "nvme2:vm-9001-disk-0",
				NetworkBridge: "vmbr0",
			},
			expected: `name=kube-1
memory=4096
cores=4
sockets=1
ostype=l26
scsi0=nvme2:vm-9001-disk-0
boot=order=scsi0
net0=virtio,bridge=vmbr0
`,
		},
		{
			name: "flatcar-blank-fallback",
			kind: "flatcar",
			config: VMConfig{
				Name: "kube-2", Memory: 2048, Cores: 2, Sockets: 1,
				BootStorage: "local", BootDiskSize: 0,
				NetworkBridge: "vmbr0",
			},
			expected: `name=kube-2
memory=2048
cores=2
sockets=1
ostype=l26
scsi0=local:200
boot=order=scsi0
net0=virtio,bridge=vmbr0
`,
		},
		{
			name: "cloudinit-full",
			kind: "cloudinit",
			config: VMConfig{
				Name: "ci-0", Memory: 4096, Cores: 2, Sockets: 1,
				BootStorage: "local-lvm", BootDiskSize: 20,
				ImageDiskPath: "/var/lib/vz/template/debian.qcow2",
				NetworkBridge: "vmbr0", NetworkMTU: 1500, VLANID: 100,
				MacAddress:  "00:11:22:33:44:55",
				StartOnBoot: true,
			},
			ci: CloudInitConfig{
				User: "debian", SSHKeys: "ssh-ed25519 AAAA test@host",
				IPConfig:   "ip=10.0.0.5/24,gw=10.0.0.1",
				Nameserver: "1.1.1.1", SearchDom: "example.com",
			},
			expected: `name=ci-0
memory=4096
cores=2
sockets=1
ostype=l26
cpu=host
scsihw=virtio-scsi-single
agent=enabled=1
serial0=socket
vga=serial0
scsi0=local-lvm:20,import-from=/var/lib/vz/template/debian.qcow2
boot=order=scsi0
ide2=local-lvm:cloudinit
ciuser=debian
sshkeys=ssh-ed25519+AAAA+test%40host
ipconfig0=ip=10.0.0.5/24,gw=10.0.0.1
nameserver=1.1.1.1
searchdomain=example.com
net0=virtio=00:11:22:33:44:55,bridge=vmbr0,mtu=1500,tag=100
onboot=1
`,
		},
		{
			name: "cloudinit-minimal",
			kind: "cloudinit",
			config: VMConfig{
				Name: "ci-1", Memory: 1024, Cores: 1, Sockets: 1,
				BootStorage: "local-lvm", BootDiskSize: 0,
				NetworkBridge: "vmbr0",
			},
			expected: `name=ci-1
memory=1024
cores=1
sockets=1
ostype=l26
cpu=host
scsihw=virtio-scsi-single
agent=enabled=1
serial0=socket
vga=serial0
scsi0=local-lvm:200
boot=order=scsi0
ide2=local-lvm:cloudinit
ipconfig0=ip=dhcp
net0=virtio,bridge=vmbr0
`,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var opts []proxmox.VirtualMachineOption
			switch tt.kind {
			case "talos":
				opts = manager.buildVMOptions(tt.config)
			case "flatcar":
				opts = manager.buildFlatcarVMOptions(tt.config)
			case "cloudinit":
				opts = manager.buildCloudInitVMOptions(tt.config, tt.ci)
			default:
				t.Fatalf("unknown builder kind %q", tt.kind)
			}

			require.Equal(t, tt.expected, formatVMOptionsForCharacterization(opts))
		})
	}
}

func formatVMOptionsForCharacterization(opts []proxmox.VirtualMachineOption) string {
	var b strings.Builder
	for _, opt := range opts {
		fmt.Fprintf(&b, "%s=%v\n", opt.Name, opt.Value)
	}
	return b.String()
}
