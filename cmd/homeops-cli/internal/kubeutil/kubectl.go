package kubeutil

import (
	"context"
	"encoding/json"
	"fmt"
)

// OutputFunc runs kubectl with the supplied arguments and returns stdout.
type OutputFunc func(context.Context, ...string) ([]byte, error)

// ScopedGetArgs builds the canonical namespaced or all-namespaces kubectl get
// invocation used by read-only JSON collectors.
func ScopedGetArgs(namespace, resource string) []string {
	args := []string{"get", resource}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	} else {
		args = append(args, "-A")
	}
	return append(args, "-o", "json")
}

// GetJSON runs a scoped kubectl get and unmarshals its JSON response.
func GetJSON(ctx context.Context, output OutputFunc, namespace, resource string, dest any) error {
	return GetJSONWithArgs(ctx, output, resource, dest, ScopedGetArgs(namespace, resource)...)
}

// GetClusterJSON runs a cluster-scoped kubectl get and unmarshals its JSON response.
func GetClusterJSON(ctx context.Context, output OutputFunc, resource string, dest any) error {
	return GetJSONWithArgs(ctx, output, resource, dest, "get", resource, "-o", "json")
}

// GetJSONWithArgs runs an explicitly shaped kubectl get seam and unmarshals
// its JSON response. The resource is used only for stable error context.
func GetJSONWithArgs(ctx context.Context, output OutputFunc, resource string, dest any, args ...string) error {
	raw, err := output(ctx, args...)
	if err != nil {
		return fmt.Errorf("kubectl get %s: %w", resource, err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("parse kubectl %s json: %w", resource, err)
	}
	return nil
}
