package ui

import (
	"encoding/json"
	"fmt"
)

// ValidateOutputFormat validates the shared table/JSON output contract.
func ValidateOutputFormat(format string) error {
	if format == "table" || format == "json" {
		return nil
	}
	return fmt.Errorf("unsupported output format %q (table, json)", format)
}

// RenderJSON renders v using the CLI's canonical indented JSON format.
func RenderJSON(v any) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
