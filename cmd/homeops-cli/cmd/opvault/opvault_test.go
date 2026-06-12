package opvault

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

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

func TestOpEditBuildsAssignments(t *testing.T) {
	argvRun, _, _ := stubOp(t, []byte("{}"))
	cmd := newEditCommand()
	cmd.SetArgs([]string{"svc", "--vault", "V", "--field", "HOST=nas01.example.com"})
	require.NoError(t, cmd.Execute())
	assert.Equal(t, []string{"item", "edit", "svc", "--vault", "V", "HOST=nas01.example.com"}, *argvRun)
}

func TestOpListErrorsPropagate(t *testing.T) {
	orig := runOpFn
	runOpFn = func(args ...string) ([]byte, error) { return nil, errors.New("not signed in") }
	t.Cleanup(func() { runOpFn = orig })
	cmd := newListCommand()
	cmd.SetArgs([]string{})
	require.Error(t, cmd.Execute())
}
