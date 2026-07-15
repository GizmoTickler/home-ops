package cmdutil

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestResolveDurationFlagDefault(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	var value time.Duration
	cmd.Flags().DurationVar(&value, "timeout", 0, "timeout")
	ResolveDurationFlagDefault(cmd, "timeout", &value, func() time.Duration { return 5 * time.Minute })
	assert.Equal(t, 5*time.Minute, value)

	assert.NoError(t, cmd.Flags().Set("timeout", "30s"))
	ResolveDurationFlagDefault(cmd, "timeout", &value, func() time.Duration { return time.Hour })
	assert.Equal(t, 30*time.Second, value)
}
