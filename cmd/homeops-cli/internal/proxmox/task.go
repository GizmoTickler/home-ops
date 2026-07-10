package proxmox

import (
	"fmt"
	"time"

	"github.com/luthermonson/go-proxmox"
)

// waitTask blocks until a Proxmox task completes, wrapping any failure with the
// operation name and VM for a consistent, contextual error. A nil task is
// treated as success because some go-proxmox calls complete synchronously and
// return no task to poll.
func (vm *VMManager) waitTask(task *proxmox.Task, timeout time.Duration, op, name string) error {
	if task == nil {
		return nil
	}
	if err := task.Wait(vm.client.Context(), time.Second, timeout); err != nil {
		return fmt.Errorf("%s task for %s: %w", op, name, err)
	}
	return nil
}
