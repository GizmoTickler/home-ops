package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOutputFormat(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		require.NoError(t, ValidateOutputFormat(format))
	}
	err := ValidateOutputFormat("yaml")
	require.EqualError(t, err, `unsupported output format "yaml" (table, json)`)
	require.EqualError(t, ValidateOutputFormat(""), `unsupported output format "" (table, json)`)
}

func TestRenderJSON(t *testing.T) {
	rendered, err := RenderJSON(map[string]any{"name": "fugu", "ready": true})
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"name\": \"fugu\",\n  \"ready\": true\n}", rendered)
}
