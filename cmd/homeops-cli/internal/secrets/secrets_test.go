package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"homeops-cli/internal/common"
)

func TestResolveRejectsUnknownScheme(t *testing.T) {
	_, err := Resolve("not-a-reference")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no recognised scheme")

	_, err = Resolve("vault://x/y")
	require.Error(t, err)
}

func TestResolveEnvProvider(t *testing.T) {
	t.Setenv("HOMEOPS_SECRET_TEST", "  value  ")
	v, err := Resolve("env://HOMEOPS_SECRET_TEST")
	require.NoError(t, err)
	assert.Equal(t, "value", v)

	_, err = Resolve("env://HOMEOPS_SECRET_TEST_MISSING")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HOMEOPS_SECRET_TEST_MISSING")
}

func TestResolveFileProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(path, []byte("s3cret\n"), 0o600))

	v, err := Resolve("file://" + path)
	require.NoError(t, err)
	assert.Equal(t, "s3cret", v)

	_, err = Resolve("file://" + filepath.Join(dir, "missing"))
	require.Error(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty"), []byte("  \n"), 0o600))
	_, err = Resolve("file://" + filepath.Join(dir, "empty"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestResolveLiteralProvider(t *testing.T) {
	v, err := Resolve("literal://core")
	require.NoError(t, err)
	assert.Equal(t, "core", v)
}

func TestResolveCmdProvider(t *testing.T) {
	v, err := Resolve("cmd://printf hello")
	require.NoError(t, err)
	assert.Equal(t, "hello", v)

	_, err = Resolve("cmd://false")
	require.Error(t, err)

	_, err = Resolve("cmd://true")
	require.Error(t, err) // no output
	assert.Contains(t, err.Error(), "no output")
}

func TestResolveOpProvider(t *testing.T) {
	restore := SetOpReadFnForTesting(func(reference string) (common.CommandResult, error) {
		if reference == "op://Vault/Item/field" {
			return common.CommandResult{Stdout: "  op-value \n"}, nil
		}
		return common.CommandResult{Stderr: "\"x\" isn't an item. not found"}, errors.New("exit 1")
	})
	defer restore()

	v, err := Resolve("op://Vault/Item/field")
	require.NoError(t, err)
	assert.Equal(t, "op-value", v)

	_, err = Resolve("op://Vault/Item/missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveOpProviderMissingBinaryHint(t *testing.T) {
	restore := SetOpReadFnForTesting(func(reference string) (common.CommandResult, error) {
		return common.CommandResult{Stderr: `exec: "op": executable file not found in $PATH`}, errors.New("exec failure")
	})
	defer restore()

	_, err := Resolve("op://Vault/Item/field")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed")
	assert.Contains(t, err.Error(), "env://")
}

func TestSecretIndirection(t *testing.T) {
	t.Setenv("HOMEOPS_INDIRECT", "resolved")
	RegisterKeymap(func(key string) (string, bool) {
		switch key {
		case "my_key":
			return "env://HOMEOPS_INDIRECT", true
		case "loop":
			return "secret://loop", true
		}
		return "", false
	})
	defer RegisterKeymap(nil)

	v, err := Resolve("secret://my_key")
	require.NoError(t, err)
	assert.Equal(t, "resolved", v)

	_, err = Resolve("secret://unknown_key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not defined")

	_, err = Resolve("secret://loop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one level of indirection")
}

func TestResolverCachesLookups(t *testing.T) {
	var calls int32
	r := NewResolverWithFunc(func(ref string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "v:" + ref, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := r.Resolve("env://X")
			assert.NoError(t, err)
			assert.Equal(t, "v:env://X", v)
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestResolverCachesFailures(t *testing.T) {
	var calls int32
	r := NewResolverWithFunc(func(ref string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", fmt.Errorf("boom")
	})
	_, err1 := r.Resolve("env://X")
	_, err2 := r.Resolve("env://X")
	require.Error(t, err1)
	require.Error(t, err2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestInjectMultiScheme(t *testing.T) {
	t.Setenv("INJ_ONE", "alpha")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "two"), []byte("beta"), 0o600))

	content := fmt.Sprintf("a: env://INJ_ONE\nb: file://%s/two\nc: plain\n", dir)
	out, err := Inject(content)
	require.NoError(t, err)
	assert.Equal(t, "a: alpha\nb: beta\nc: plain\n", out)
}

func TestInjectReportsAllFailures(t *testing.T) {
	content := "a: env://INJ_MISSING_ONE\nb: env://INJ_MISSING_TWO\n"
	_, err := Inject(content)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INJ_MISSING_ONE")
	assert.Contains(t, err.Error(), "INJ_MISSING_TWO")
}

func TestInjectNoReferences(t *testing.T) {
	out, err := Inject("plain: content\n")
	require.NoError(t, err)
	assert.Equal(t, "plain: content\n", out)
}

func TestInjectSecretSchemeViaKeymap(t *testing.T) {
	t.Setenv("INJ_KEYED", "gamma")
	RegisterKeymap(func(key string) (string, bool) {
		if key == "keyed" {
			return "env://INJ_KEYED", true
		}
		return "", false
	})
	defer RegisterKeymap(nil)

	out, err := Inject("value: secret://keyed\nurl: https://passwords.secret://keyed\n")
	require.NoError(t, err)
	assert.Equal(t, "value: gamma\nurl: https://passwords.gamma\n", out)
}

func TestListReferences(t *testing.T) {
	refs := ListReferences("a: env://B\nb: env://A\nc: env://A\nd: cmd://not inline\n")
	assert.Equal(t, []string{"env://A", "env://B"}, refs)
}

func TestIsReference(t *testing.T) {
	assert.True(t, IsReference("op://v/i/f"))
	assert.True(t, IsReference("env://X"))
	assert.True(t, IsReference("literal://x"))
	assert.False(t, IsReference("https://example.com"))
	assert.False(t, IsReference("plain"))
}

func TestResolveBatch(t *testing.T) {
	t.Setenv("BATCH_ONE", "1")
	t.Setenv("BATCH_TWO", "2")
	results := ResolveBatch([]string{"env://BATCH_ONE", "env://BATCH_TWO", "env://BATCH_MISSING"})
	assert.Equal(t, map[string]string{
		"env://BATCH_ONE": "1",
		"env://BATCH_TWO": "2",
	}, results)
	assert.Empty(t, ResolveBatch(nil))
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	got, err := ExpandHome("~/x/y")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "x", "y"), got)

	got, err = ExpandHome("/abs/path")
	require.NoError(t, err)
	assert.Equal(t, "/abs/path", got)
}
