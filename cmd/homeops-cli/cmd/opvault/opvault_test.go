package opvault

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/testutil"
)

func stubOp(t *testing.T, out []byte) (*[]string, *[]byte, *[]string) {
	t.Helper()
	var argvRun, argvStdin []string
	var stdin []byte
	origRun, origStdin, origConfirm := runOpFn, runOpStdinFn, confirmFn
	runOpFn = func(args ...string) ([]byte, error) { argvRun = args; return out, nil }
	runOpStdinFn = func(in []byte, args ...string) ([]byte, error) { stdin = in; argvStdin = args; return out, nil }
	confirmFn = func(string, bool) (bool, error) { return true, nil }
	t.Cleanup(func() { runOpFn, runOpStdinFn, confirmFn = origRun, origStdin, origConfirm })
	return &argvRun, &stdin, &argvStdin
}

func TestOpCreateSendsValuesViaStdinOnly(t *testing.T) {
	argvRun, stdin, argvStdin := stubOp(t, []byte("{}"))

	cmd := newCreateCommand()
	cmd.SetArgs([]string{"my-svc", "--vault", "Infrastructure",
		"--field", "API_HOST=10.0.0.5", "--field", "API_TOKEN=supersecret"})
	require.NoError(t, cmd.Execute())

	assert.Nil(t, *argvRun, "create must not use argv execution")
	assert.Equal(t, []string{"item", "create", "--vault", "Infrastructure"}, *argvStdin)
	assert.NotContains(t, strings.Join(*argvStdin, " "), "supersecret")

	var tmpl struct {
		Title  string `json:"title"`
		Fields []struct {
			Label string `json:"label"`
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(*stdin, &tmpl))
	assert.Equal(t, "my-svc", tmpl.Title)
	types := map[string]string{}
	for _, f := range tmpl.Fields {
		types[f.Label] = f.Type
	}
	assert.Equal(t, "STRING", types["API_HOST"])
	assert.Equal(t, "CONCEALED", types["API_TOKEN"], "token-like labels must be concealed")
}

func TestOpGetMasksSecretsByDefault(t *testing.T) {
	item := `{"title":"svc","fields":[
		{"label":"HOST","type":"STRING","value":"10.0.0.5"},
		{"label":"API_TOKEN","type":"CONCEALED","value":"supersecret"}]}`
	stubOp(t, []byte(item))

	stdout, _, err := testutil.CaptureOutput(func() {
		cmd := newGetCommand()
		cmd.SetArgs([]string{"svc"})
		require.NoError(t, cmd.Execute())
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "10.0.0.5")
	assert.Contains(t, stdout, "********")
	assert.NotContains(t, stdout, "supersecret")
}

func TestOpGetRevealAndSingleField(t *testing.T) {
	item := `{"title":"svc","fields":[{"label":"API_TOKEN","type":"CONCEALED","value":"supersecret"}]}`
	stubOp(t, []byte(item))

	stdout, _, err := testutil.CaptureOutput(func() {
		cmd := newGetCommand()
		cmd.SetArgs([]string{"svc", "--field", "API_TOKEN", "--reveal"})
		require.NoError(t, cmd.Execute())
	})
	require.NoError(t, err)
	assert.Equal(t, "supersecret", strings.TrimSpace(stdout))
}

func TestOpDeleteRespectsDeclinedConfirm(t *testing.T) {
	argvRun, _, _ := stubOp(t, []byte("{}"))
	origConfirm := confirmFn
	confirmFn = func(string, bool) (bool, error) { return false, nil }
	t.Cleanup(func() { confirmFn = origConfirm })

	cmd := newDeleteCommand()
	cmd.SetArgs([]string{"old-item"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
	assert.Nil(t, *argvRun, "op must not run when confirmation is declined")
}

func TestOpDeleteArchive(t *testing.T) {
	argvRun, _, _ := stubOp(t, []byte("{}"))
	cmd := newDeleteCommand()
	cmd.SetArgs([]string{"old-item", "--vault", "V", "--archive"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"item", "delete", "old-item", "--vault", "V", "--archive"}, *argvRun)
}

func TestParseFieldsRejectsBadPairs(t *testing.T) {
	_, err := parseFields([]string{"novalue"})
	require.Error(t, err)
	_, err = parseFields([]string{"=x"})
	require.Error(t, err)
}

func TestOpEditSendsValuesViaStdinTemplateOnly(t *testing.T) {
	argvRun, stdin, argvStdin := stubOp(t, []byte("{}"))
	cmd := newEditCommand()
	cmd.SetArgs([]string{"svc", "--vault", "V",
		"--field", "HOST=nas01.example.com",
		"--field", "API_TOKEN=supersecret",
	})
	require.NoError(t, cmd.Execute())

	assert.Nil(t, *argvRun, "edit must not use argv assignments for field values")
	assert.Equal(t, []string{"item", "edit", "svc", "--vault", "V"}, *argvStdin)
	assert.NotContains(t, strings.Join(*argvStdin, " "), "supersecret")
	assert.NotContains(t, strings.Join(*argvStdin, " "), "nas01.example.com")

	var tmpl struct {
		Fields []struct {
			Label string `json:"label"`
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(*stdin, &tmpl))
	require.Len(t, tmpl.Fields, 2)
	types := map[string]string{}
	values := map[string]string{}
	for _, f := range tmpl.Fields {
		types[f.Label] = f.Type
		values[f.Label] = f.Value
	}
	assert.Equal(t, "STRING", types["HOST"])
	assert.Equal(t, "CONCEALED", types["API_TOKEN"])
	assert.Equal(t, "nas01.example.com", values["HOST"])
	assert.Equal(t, "supersecret", values["API_TOKEN"])
}

func TestOpListErrorsPropagate(t *testing.T) {
	orig := runOpFn
	runOpFn = func(args ...string) ([]byte, error) { return nil, errors.New("not signed in") }
	t.Cleanup(func() { runOpFn = orig })
	cmd := newListCommand()
	cmd.SetArgs([]string{})
	require.Error(t, cmd.Execute())
}

func TestOpVaultsList(t *testing.T) {
	argvRun, _, _ := stubOp(t, []byte(`[{"id":"v2","name":"Private"},{"id":"v1","name":"Infrastructure"}]`))

	cmd := newVaultsCommand()
	cmd.SetArgs([]string{"list"})
	out, _, err := testutil.CaptureOutput(func() { require.NoError(t, cmd.Execute()) })
	require.NoError(t, err)

	assert.Equal(t, []string{"vault", "list", "--format=json"}, *argvRun)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 3) // header + 2 vaults
	assert.Contains(t, lines[0], "NAME")
	assert.Contains(t, lines[1], "Infrastructure") // sorted by name
	assert.Contains(t, lines[2], "Private")
}

func TestOpMove(t *testing.T) {
	argvRun, _, _ := stubOp(t, []byte("{}"))

	cmd := newMoveCommand()
	cmd.SetArgs([]string{"my-svc", "--vault", "Private", "--to-vault", "Infrastructure"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"item", "move", "my-svc", "--destination-vault", "Infrastructure", "--current-vault", "Private"}, *argvRun)
}

func TestOpMoveRequiresDestinationAndConfirm(t *testing.T) {
	argvRun, _, _ := stubOp(t, []byte("{}"))

	cmd := newMoveCommand()
	cmd.SetArgs([]string{"my-svc"})
	require.ErrorContains(t, cmd.Execute(), "--to-vault is required")

	confirmFn = func(string, bool) (bool, error) { return false, nil }
	cmd = newMoveCommand()
	cmd.SetArgs([]string{"my-svc", "--to-vault", "Infrastructure"})
	require.ErrorContains(t, cmd.Execute(), "cancelled")
	assert.Nil(t, *argvRun, "declined confirm must not run op")
}

func TestOpDuplicate(t *testing.T) {
	item := `{"title":"my-svc","category":"SECURE_NOTE","fields":[
		{"label":"HOST","type":"STRING","value":"10.0.0.5"},
		{"label":"API_TOKEN","type":"CONCEALED","value":"supersecret"},
		{"label":"","type":"STRING","value":"ignored"},
		{"label":"notes","type":"OTP","value":"weird"}]}`
	argvRun, stdin, argvStdin := stubOp(t, []byte(item))

	cmd := newDuplicateCommand()
	cmd.SetArgs([]string{"my-svc", "--vault", "Private", "--to-vault", "Staging", "--name", "my-svc-copy"})
	require.NoError(t, cmd.Execute())

	// source read with --reveal from the right vault
	assert.Equal(t, []string{"item", "get", "my-svc", "--format=json", "--reveal", "--vault", "Private"}, *argvRun)
	// copy created via stdin template into the destination vault
	assert.Equal(t, []string{"item", "create", "--vault", "Staging"}, *argvStdin)
	assert.NotContains(t, strings.Join(*argvStdin, " "), "supersecret")

	var tmpl struct {
		Title    string `json:"title"`
		Category string `json:"category"`
		Fields   []struct {
			Label string `json:"label"`
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(*stdin, &tmpl))
	assert.Equal(t, "my-svc-copy", tmpl.Title)
	assert.Equal(t, "SECURE_NOTE", tmpl.Category)
	types := map[string]string{}
	for _, f := range tmpl.Fields {
		types[f.Label] = f.Type
	}
	assert.Equal(t, "STRING", types["HOST"])
	assert.Equal(t, "CONCEALED", types["API_TOKEN"])
	assert.Equal(t, "STRING", types["notes"], "unsupported field types degrade to STRING")
	assert.NotContains(t, types, "", "unlabeled fields are skipped")
}

func TestOpDuplicateNeedsTarget(t *testing.T) {
	stubOp(t, []byte("{}"))
	cmd := newDuplicateCommand()
	cmd.SetArgs([]string{"my-svc"})
	require.ErrorContains(t, cmd.Execute(), "--to-vault and/or --name")
}

func TestOpCommandErrorSurfacesStderr(t *testing.T) {
	exitErr := &exec.ExitError{
		Stderr: []byte(`[ERROR] 2026/06/12 19:40:12 "ghost" isn't an item. Specify the item with its UUID, name, or domain.`),
	}
	err := opCommandError([]string{"item", "get", "ghost", "--format=json"}, exitErr)
	assert.Equal(t, `op item get: "ghost" isn't an item. Specify the item with its UUID, name, or domain.`, err.Error())

	// no stderr: keep the wrapped error
	err = opCommandError([]string{"vault", "list"}, errors.New("exit status 1"))
	assert.Equal(t, "op vault list: exit status 1", err.Error())
}

func TestRunOpItemAutoDetectsVaultForServiceAccounts(t *testing.T) {
	origRun := runOpFn
	t.Cleanup(func() { runOpFn = origRun })

	serviceAccountErr := errors.New(`op item get: a vault query must be provided when this command is called by a service account`)
	var calls [][]string
	runOpFn = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "item get ghost"):
			if strings.Contains(joined, "--vault") {
				return []byte(`{"title":"ghost"}`), nil
			}
			return nil, serviceAccountErr
		case joined == "vault list --format=json":
			return []byte(`[{"id":"v1","name":"Infrastructure"},{"id":"v2","name":"Private"}]`), nil
		case strings.HasPrefix(joined, "item list --vault Infrastructure"):
			return []byte(`[{"id":"abc","title":"Ghost"}]`), nil
		case strings.HasPrefix(joined, "item list --vault Private"):
			return []byte(`[]`), nil
		}
		return nil, fmt.Errorf("unexpected op call: %s", joined)
	}

	out, err := runOpItem("ghost", "", func(v string) []string {
		args := []string{"item", "get", "ghost", "--format=json"}
		if v != "" {
			args = append(args, "--vault", v)
		}
		return args
	})
	require.NoError(t, err)
	assert.Contains(t, string(out), "ghost")
	// last call carries the detected vault (case-insensitive title match)
	last := calls[len(calls)-1]
	assert.Contains(t, strings.Join(last, " "), "--vault Infrastructure")
}

func TestRunOpItemAmbiguousVault(t *testing.T) {
	origRun := runOpFn
	t.Cleanup(func() { runOpFn = origRun })

	runOpFn = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "item get dup"):
			return nil, errors.New("a vault query must be provided when this command is called by a service account")
		case joined == "vault list --format=json":
			return []byte(`[{"id":"v1","name":"A"},{"id":"v2","name":"B"}]`), nil
		case strings.HasPrefix(joined, "item list"):
			return []byte(`[{"id":"x","title":"dup"}]`), nil
		}
		return nil, fmt.Errorf("unexpected op call: %s", joined)
	}

	_, err := runOpItem("dup", "", func(v string) []string {
		args := []string{"item", "get", "dup"}
		if v != "" {
			args = append(args, "--vault", v)
		}
		return args
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple vaults")
	assert.Contains(t, err.Error(), "A, B")
}

func TestRunOpItemExplicitVaultSkipsDetection(t *testing.T) {
	origRun := runOpFn
	t.Cleanup(func() { runOpFn = origRun })

	var calls int
	runOpFn = func(args ...string) ([]byte, error) {
		calls++
		return []byte("{}"), nil
	}
	_, err := runOpItem("thing", "Infrastructure", func(v string) []string {
		return []string{"item", "get", "thing", "--vault", v}
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestOpRevealShowsClearTextAndSingleField(t *testing.T) {
	item := `{"title":"svc","fields":[
		{"label":"HOST","type":"STRING","value":"10.0.0.5"},
		{"label":"API_TOKEN","type":"CONCEALED","value":"supersecret"}]}`
	stubOp(t, []byte(item))

	cmd := newRevealCommand()
	cmd.SetArgs([]string{"svc"})
	stdout, _, err := testutil.CaptureOutput(func() { require.NoError(t, cmd.Execute()) })
	require.NoError(t, err)
	assert.Contains(t, stdout, "supersecret", "reveal must not mask")
	assert.NotContains(t, stdout, "********")

	// field as the second positional prints the bare value
	cmd = newRevealCommand()
	cmd.SetArgs([]string{"svc", "API_TOKEN"})
	stdout, _, err = testutil.CaptureOutput(func() { require.NoError(t, cmd.Execute()) })
	require.NoError(t, err)
	assert.Equal(t, "supersecret", strings.TrimSpace(stdout))
}

func TestVaultCompletionWiredEverywhere(t *testing.T) {
	var check func(cmd *cobra.Command)
	root := NewCommand()
	check = func(cmd *cobra.Command) {
		for _, flagName := range []string{"vault", "to-vault"} {
			if cmd.Flags().Lookup(flagName) == nil {
				continue
			}
			_, exists := cmd.GetFlagCompletionFunc(flagName)
			assert.True(t, exists, "command %q must complete --%s", cmd.CommandPath(), flagName)
		}
		for _, sub := range cmd.Commands() {
			check(sub)
		}
	}
	check(root)
}
