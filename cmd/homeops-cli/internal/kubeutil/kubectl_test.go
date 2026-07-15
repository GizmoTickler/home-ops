package kubeutil

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/config"
)

func TestGetJSONUsesScopedArguments(t *testing.T) {
	var got string
	var decoded struct {
		Items []string `json:"items"`
	}
	err := GetJSON(context.Background(), func(_ context.Context, args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte(`{"items":["one"]}`), nil
	}, "media", "pods", &decoded)
	require.NoError(t, err)
	assert.Equal(t, "get pods --namespace media -o json", got)
	assert.Equal(t, []string{"one"}, decoded.Items)
}

func TestNodeSSHConfig(t *testing.T) {
	restore := config.SetForTesting(&config.Config{Cluster: config.ClusterConfig{NodeSSHPort: 2222}})
	t.Cleanup(restore)
	assert.Equal(t, "192.0.2.10", NodeSSHConfig(config.Node{IP: "192.0.2.10"}, "core").Host)
	assert.Equal(t, "core", NodeSSHConfig(config.Node{}, "core").Username)
	assert.Equal(t, "2222", NodeSSHConfig(config.Node{}, "core").Port)
}
