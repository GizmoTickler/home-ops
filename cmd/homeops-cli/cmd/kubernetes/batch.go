package kubernetes

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// batchResult records best-effort bulk operation results. It is safe for the
// parallel Flux reconciliation path and keeps the user-facing error stable by
// sorting failed resource names before reporting them.
type batchResult struct {
	mu        sync.Mutex
	succeeded int
	failed    []string
}

func (r *batchResult) addSuccess() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.succeeded++
}

func (r *batchResult) addFailure(resource string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, resource)
}

func (r *batchResult) counts() (succeeded, failed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.succeeded, len(r.failed)
}

func (r *batchResult) err(operation string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.failed) == 0 {
		return nil
	}

	failed := append([]string(nil), r.failed...)
	sort.Strings(failed)
	return fmt.Errorf("%s failed for %d resource(s): %s", operation, len(failed), strings.Join(failed, ", "))
}
