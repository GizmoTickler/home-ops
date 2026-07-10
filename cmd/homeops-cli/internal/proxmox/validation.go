package proxmox

import "fmt"

func requireVMName(name string) error {
	if name == "" {
		return fmt.Errorf("VM name is required")
	}
	return nil
}

func requireSnapshotName(name string) error {
	if name == "" {
		return fmt.Errorf("snapshot name is required")
	}
	return nil
}

func requireTargetVMName(name string) error {
	if name == "" {
		return fmt.Errorf("target VM name is required")
	}
	return nil
}
