package onepassword

import (
	"fmt"
	"strings"
)

// ExtractReferences finds all 1Password references in the content
func ExtractReferences(content string) []string {
	var refs []string

	// Find all op:// references using a more comprehensive approach
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, "op://") {
			// Find all op:// references in this line
			for i := 0; i < len(line); i++ {
				if strings.HasPrefix(line[i:], "op://") {
					start := i
					// Find the end of the reference
					end := len(line)
					for j := start + 5; j < len(line); j++ {
						if line[j] == ' ' || line[j] == '"' || line[j] == '\'' || line[j] == '}' || line[j] == ',' || line[j] == '\n' || line[j] == '\t' || line[j] == ':' {
							end = j
							break
						}
					}
					ref := line[start:end]
					if len(ref) > 5 { // Ensure we have more than just "op://"
						refs = append(refs, ref)
					}
					// Move past this reference
					i = end - 1
				}
			}
		}
	}

	// Deduplicate references
	seen := make(map[string]bool)
	var uniqueRefs []string
	for _, ref := range refs {
		if !seen[ref] {
			seen[ref] = true
			uniqueRefs = append(uniqueRefs, ref)
		}
	}

	return uniqueRefs
}

// ValidateReferenceFormat validates the format of a 1Password reference
func ValidateReferenceFormat(ref string) error {
	if !strings.HasPrefix(ref, "op://") {
		return fmt.Errorf("reference must start with 'op://'")
	}

	// Remove the op:// prefix
	path := strings.TrimPrefix(ref, "op://")
	parts := strings.Split(path, "/")

	// Should have at least vault/item format
	if len(parts) < 2 {
		return fmt.Errorf("reference must have format 'op://vault/item' or 'op://vault/item/field'")
	}

	// Validate vault name
	if parts[0] == "" {
		return fmt.Errorf("vault name cannot be empty")
	}

	// Validate item name
	if parts[1] == "" {
		return fmt.Errorf("item name cannot be empty")
	}

	// If field is specified, validate it's not empty
	if len(parts) >= 3 && parts[2] == "" {
		return fmt.Errorf("field name cannot be empty")
	}

	return nil
}
