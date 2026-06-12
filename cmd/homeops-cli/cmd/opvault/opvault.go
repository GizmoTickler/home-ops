// Package opvault implements the `homeops-cli op` command group: 1Password
// item management through the op CLI. Field values travel via stdin item
// templates (never argv) and are masked on output unless --reveal.
package opvault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"homeops-cli/internal/common"
	"homeops-cli/internal/ui"
)

// runOpFn executes op with args (no stdin). Swappable for tests.
var runOpFn = func(args ...string) ([]byte, error) {
	c := common.Command("op", args...)
	c.Stdin = nil
	out, err := c.Output()
	if err != nil {
		return nil, opCommandError(args, err)
	}
	return out, nil
}

// runOpStdinFn executes op with an item template piped on stdin so secret
// values never appear in argv. Swappable for tests.
var runOpStdinFn = func(stdin []byte, args ...string) ([]byte, error) {
	c := common.Command("op", args...)
	c.Stdin = bytes.NewReader(stdin)
	out, err := c.Output()
	if err != nil {
		return nil, opCommandError(args, err)
	}
	return out, nil
}

// opErrorPrefix strips op's "[ERROR] 2026/06/12 19:40:12 " stderr preamble.
var opErrorPrefix = regexp.MustCompile(`^\[ERROR\]\s+\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}\s+`)

// opCommandError surfaces op's actual stderr message ("isn't an item",
// "no account found", ...) instead of a bare "exit status 1".
func opCommandError(args []string, err error) error {
	context := "op"
	if len(args) >= 2 {
		context = "op " + strings.Join(args[:2], " ")
	} else if len(args) == 1 {
		context = "op " + args[0]
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		for _, line := range strings.Split(strings.TrimSpace(string(exitErr.Stderr)), "\n") {
			line = strings.TrimSpace(opErrorPrefix.ReplaceAllString(strings.TrimSpace(line), ""))
			if line != "" {
				return fmt.Errorf("%s: %s", context, line)
			}
		}
	}
	return fmt.Errorf("%s: %w", context, err)
}

var confirmFn = ui.Confirm

// NewCommand builds the `op` command group.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "op",
		Short: "Manage 1Password items (list, get, create, edit, delete)",
		Long: `Create, inspect, edit, and delete 1Password items through the op CLI.
Secret values are passed via stdin templates (never command arguments) and are
masked on output unless --reveal is given.`,
	}
	cmd.AddCommand(newListCommand(), newGetCommand(), newCreateCommand(), newEditCommand(), newDeleteCommand(),
		newVaultsCommand(), newMoveCommand(), newDuplicateCommand())
	return cmd
}

func newVaultsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vaults",
		Short: "Work with vaults",
	}
	list := &cobra.Command{
		Use:     "list",
		Short:   "List available vaults",
		Example: `  homeops-cli op vaults list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := runOpFn("vault", "list", "--format=json")
			if err != nil {
				return err
			}
			var vaults []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(out, &vaults); err != nil {
				return fmt.Errorf("parse op output: %w", err)
			}
			sort.Slice(vaults, func(i, j int) bool { return vaults[i].Name < vaults[j].Name })
			rows := make([][]string, 0, len(vaults))
			for _, v := range vaults {
				rows = append(rows, []string{v.Name, v.ID})
			}
			ui.PrintTable([]string{"NAME", "ID"}, rows)
			return nil
		},
	}
	cmd.AddCommand(list)
	return cmd
}

func newMoveCommand() *cobra.Command {
	var vault, toVault string
	cmd := &cobra.Command{
		Use:   "move <item>",
		Short: "Move an item to another vault",
		Long: `Move an item between vaults via 'op item move'. 1Password implements a
move as copy + delete, so the item gets a new ID in the destination vault.`,
		Args:    cobra.ExactArgs(1),
		Example: `  homeops-cli op move my-service --vault Private --to-vault Infrastructure`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if toVault == "" {
				return fmt.Errorf("--to-vault is required")
			}
			ok, err := confirmFn(fmt.Sprintf("Move item %q to vault %q? (it gets a new item ID)", args[0], toVault), false)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("cancelled by user")
			}
			opArgs := []string{"item", "move", args[0], "--destination-vault", toVault}
			if vault != "" {
				opArgs = append(opArgs, "--current-vault", vault)
			}
			if _, err := runOpFn(opArgs...); err != nil {
				return err
			}
			common.NewColorLogger().Success("Moved item %q to vault %q", args[0], toVault)
			return nil
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "vault currently holding the item")
	cmd.Flags().StringVar(&toVault, "to-vault", "", "destination vault (required)")
	return cmd
}

func newDuplicateCommand() *cobra.Command {
	var vault, toVault, newName string
	cmd := &cobra.Command{
		Use:   "duplicate <item>",
		Short: "Copy an item (optionally into another vault / under a new name)",
		Long: `Duplicate an item: read it as JSON and re-create it (fields travel via a
stdin template, never argv). The source item is left untouched.`,
		Args: cobra.ExactArgs(1),
		Example: `  homeops-cli op duplicate prod-creds --to-vault Staging
  homeops-cli op duplicate my-service --name my-service-copy`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if toVault == "" && newName == "" {
				return fmt.Errorf("pass --to-vault and/or --name (a same-vault same-name copy is ambiguous)")
			}
			getArgs := []string{"item", "get", args[0], "--format=json", "--reveal"}
			if vault != "" {
				getArgs = append(getArgs, "--vault", vault)
			}
			out, err := runOpFn(getArgs...)
			if err != nil {
				return err
			}
			var item struct {
				Title    string `json:"title"`
				Category string `json:"category"`
				Fields   []struct {
					Label string `json:"label"`
					Type  string `json:"type"`
					Value string `json:"value"`
				} `json:"fields"`
			}
			if err := json.Unmarshal(out, &item); err != nil {
				return fmt.Errorf("parse op output: %w", err)
			}

			fields := make([]opField, 0, len(item.Fields))
			for _, f := range item.Fields {
				if f.Label == "" || f.Value == "" {
					continue
				}
				typ := f.Type
				if typ != "STRING" && typ != "CONCEALED" {
					// Re-creation only supports the basic field types; keep
					// secrets concealed, degrade the rest to strings.
					typ = "STRING"
					if strings.EqualFold(f.Type, "CONCEALED") {
						typ = "CONCEALED"
					}
				}
				fields = append(fields, opField{Label: f.Label, Type: typ, Value: f.Value})
			}
			if len(fields) == 0 {
				return fmt.Errorf("item %q has no copyable fields", args[0])
			}

			title := item.Title
			if newName != "" {
				title = newName
			}
			category := item.Category
			if category == "" {
				category = "SECURE_NOTE"
			}
			tmpl := map[string]interface{}{"title": title, "category": category, "fields": fields}
			doc, err := json.Marshal(tmpl)
			if err != nil {
				return err
			}
			createArgs := []string{"item", "create"}
			if toVault != "" {
				createArgs = append(createArgs, "--vault", toVault)
			} else if vault != "" {
				createArgs = append(createArgs, "--vault", vault)
			}
			if _, err := runOpStdinFn(doc, createArgs...); err != nil {
				return err
			}
			dest := toVault
			if dest == "" {
				dest = "the same vault"
			}
			common.NewColorLogger().Success("Duplicated %q as %q in %s (%d fields)", args[0], title, dest, len(fields))
			return nil
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "vault holding the source item")
	cmd.Flags().StringVar(&toVault, "to-vault", "", "vault to copy into (default: the source vault)")
	cmd.Flags().StringVar(&newName, "name", "", "title for the copy (default: same as the source)")
	return cmd
}

func newListCommand() *cobra.Command {
	var vault string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List items in a vault",
		Example: `  homeops-cli op list --vault Infrastructure`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opArgs := []string{"item", "list", "--format=json"}
			if vault != "" {
				opArgs = append(opArgs, "--vault", vault)
			}
			out, err := runOpFn(opArgs...)
			if err != nil {
				return err
			}
			var items []struct {
				Title    string `json:"title"`
				Category string `json:"category"`
				Vault    struct {
					Name string `json:"name"`
				} `json:"vault"`
			}
			if err := json.Unmarshal(out, &items); err != nil {
				return fmt.Errorf("parse op output: %w", err)
			}
			sort.Slice(items, func(i, j int) bool { return items[i].Title < items[j].Title })
			rows := make([][]string, 0, len(items))
			for _, it := range items {
				rows = append(rows, []string{it.Title, strings.ToLower(it.Category), it.Vault.Name})
			}
			ui.PrintTable([]string{"TITLE", "CATEGORY", "VAULT"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "vault to list (default: all)")
	return cmd
}

func newGetCommand() *cobra.Command {
	var vault, field string
	var reveal bool
	cmd := &cobra.Command{
		Use:   "get <item>",
		Short: "Show an item's fields (values masked unless --reveal)",
		Args:  cobra.ExactArgs(1),
		Example: `  homeops-cli op get talosdeploy --vault Infrastructure
  homeops-cli op get talosdeploy --field TRUENAS_HOST --reveal`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opArgs := []string{"item", "get", args[0], "--format=json"}
			if vault != "" {
				opArgs = append(opArgs, "--vault", vault)
			}
			out, err := runOpFn(opArgs...)
			if err != nil {
				return err
			}
			var item struct {
				Title  string `json:"title"`
				Fields []struct {
					Label string `json:"label"`
					Type  string `json:"type"`
					Value string `json:"value"`
				} `json:"fields"`
			}
			if err := json.Unmarshal(out, &item); err != nil {
				return fmt.Errorf("parse op output: %w", err)
			}
			for _, f := range item.Fields {
				if field != "" && f.Label != field {
					continue
				}
				value := f.Value
				if !reveal && (f.Type == "CONCEALED" || strings.Contains(strings.ToLower(f.Label), "key") || strings.Contains(strings.ToLower(f.Label), "secret") || strings.Contains(strings.ToLower(f.Label), "password") || strings.Contains(strings.ToLower(f.Label), "token")) {
					value = "********"
				}
				if field != "" {
					fmt.Println(value)
					return nil
				}
				if f.Label != "" {
					fmt.Printf("%-32s %s\n", f.Label, value)
				}
			}
			if field != "" {
				return fmt.Errorf("field %q not found on item %q", field, args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "vault containing the item")
	cmd.Flags().StringVar(&field, "field", "", "print a single field's value")
	cmd.Flags().BoolVar(&reveal, "reveal", false, "show secret values in clear text")
	return cmd
}

type opField struct {
	Label string `json:"label,omitempty"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// parseFields turns k=v pairs into op fields; *_key/secret/password/token
// labels become CONCEALED.
func parseFields(pairs []string) ([]opField, error) {
	fields := make([]opField, 0, len(pairs))
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--field must be label=value (got %q)", kv)
		}
		typ := "STRING"
		lower := strings.ToLower(k)
		if strings.Contains(lower, "key") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "password") || strings.Contains(lower, "token") {
			typ = "CONCEALED"
		}
		fields = append(fields, opField{Label: k, Type: typ, Value: v})
	}
	return fields, nil
}

func newCreateCommand() *cobra.Command {
	var vault, category string
	var fieldPairs []string
	cmd := &cobra.Command{
		Use:   "create <item>",
		Short: "Create an item with fields (values via stdin, never argv)",
		Args:  cobra.ExactArgs(1),
		Example: `  homeops-cli op create my-service --vault Infrastructure \
    --field API_HOST=10.0.0.5 --field API_TOKEN=abc123`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fields, err := parseFields(fieldPairs)
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				return fmt.Errorf("at least one --field label=value is required")
			}
			tmpl := map[string]interface{}{"title": args[0], "category": "SECURE_NOTE", "fields": fields}
			if category != "" {
				tmpl["category"] = strings.ToUpper(category)
			}
			doc, err := json.Marshal(tmpl)
			if err != nil {
				return err
			}
			opArgs := []string{"item", "create"}
			if vault != "" {
				opArgs = append(opArgs, "--vault", vault)
			}
			if _, err := runOpStdinFn(doc, opArgs...); err != nil {
				return err
			}
			common.NewColorLogger().Success("Created item %q (%d fields)", args[0], len(fields))
			return nil
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "vault to create the item in")
	cmd.Flags().StringVar(&category, "category", "", "item category (default SECURE_NOTE)")
	cmd.Flags().StringArrayVar(&fieldPairs, "field", nil, "field as label=value (repeatable; *key/secret/password/token become concealed)")
	return cmd
}

func newEditCommand() *cobra.Command {
	var vault string
	var fieldPairs []string
	cmd := &cobra.Command{
		Use:   "edit <item>",
		Short: "Set fields on an existing item",
		Args:  cobra.ExactArgs(1),
		Example: `  homeops-cli op edit talosdeploy --vault Infrastructure \
    --field TRUENAS_HOST=nas01.example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fields, err := parseFields(fieldPairs)
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				return fmt.Errorf("at least one --field label=value is required")
			}
			// NOTE: `op item edit` only accepts field assignments as arguments,
			// so values briefly appear in this process's argv (same exposure as
			// using the op CLI directly). For new secrets prefer `op create`,
			// which passes everything via a stdin template.
			opArgs := []string{"item", "edit", args[0]}
			if vault != "" {
				opArgs = append(opArgs, "--vault", vault)
			}
			for _, f := range fields {
				opArgs = append(opArgs, fmt.Sprintf("%s=%s", f.Label, f.Value))
			}
			if _, err := runOpFn(opArgs...); err != nil {
				return err
			}
			common.NewColorLogger().Success("Updated %d field(s) on %q", len(fields), args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "vault containing the item")
	cmd.Flags().StringArrayVar(&fieldPairs, "field", nil, "field as label=value (repeatable)")
	return cmd
}

func newDeleteCommand() *cobra.Command {
	var vault string
	var archive bool
	cmd := &cobra.Command{
		Use:     "delete <item>",
		Short:   "Delete (or archive) an item",
		Args:    cobra.ExactArgs(1),
		Example: `  homeops-cli op delete old-item --vault Infrastructure --archive`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ok, err := confirmFn(fmt.Sprintf("Delete 1Password item %q?", args[0]), false)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("cancelled by user")
			}
			opArgs := []string{"item", "delete", args[0]}
			if vault != "" {
				opArgs = append(opArgs, "--vault", vault)
			}
			if archive {
				opArgs = append(opArgs, "--archive")
			}
			if _, err := runOpFn(opArgs...); err != nil {
				return err
			}
			common.NewColorLogger().Success("Deleted item %q", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "", "vault containing the item")
	cmd.Flags().BoolVar(&archive, "archive", false, "archive instead of permanent delete")
	return cmd
}
