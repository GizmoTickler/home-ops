package kubernetes

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindGitRootOutsideRepository(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(wd) }()

	require.NoError(t, os.Chdir(t.TempDir()))

	_, err = findGitRoot()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to find git repository root")
}
