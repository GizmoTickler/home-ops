package secrets

import (
	"errors"
	"os"
	"strings"
	"testing"

	"homeops-cli/internal/common"
)

func FuzzSecretReferences(f *testing.F) {
	restoreOp := SetOpReadFnForTesting(func(reference string) (common.CommandResult, error) {
		return common.CommandResult{Stderr: "not found"}, errors.New("fake op read disabled in fuzz test")
	})
	f.Cleanup(restoreOp)

	RegisterKeymap(func(key string) (string, bool) {
		switch key {
		case "ok":
			return "literal://resolved", true
		case "loop", "nested":
			return "secret://other", true
		default:
			return "", false
		}
	})
	f.Cleanup(func() { RegisterKeymap(nil) })

	if err := os.Setenv("HOMEOPS_FUZZ_SECRET_REF", "resolved"); err == nil {
		f.Cleanup(func() { _ = os.Unsetenv("HOMEOPS_FUZZ_SECRET_REF") })
	}

	for _, seed := range []string{
		"",
		"plain text",
		"op://",
		"op://Vault/Item/field",
		"env://HOMEOPS_FUZZ_SECRET_REF",
		"env://",
		"file://",
		"cmd://printf should-not-run",
		"secret://ok",
		"secret://loop",
		"secret://nested",
		"literal://value",
		"a: env://HOMEOPS_FUZZ_SECRET_REF\nb: op://Vault/Item/field\n",
		"partial op://Vault/Item",
		"nested secret://nested",
		"quote \"env://HOMEOPS_FUZZ_SECRET_REF\"",
		"unicode://☃",
		strings.Repeat("op://Vault/Item/field ", 64),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		_ = IsReference(s)
		refs := ListReferences(s)
		for i := 1; i < len(refs); i++ {
			if refs[i-1] >= refs[i] {
				t.Fatalf("ListReferences(%q) returned unsorted or duplicate refs: %#v", s, refs)
			}
		}

		// ResolveSilent is safe here for malformed strings and side-effect-free
		// providers. Avoid file:// and cmd:// because the production providers
		// intentionally touch the filesystem or shell.
		if !strings.HasPrefix(s, "file://") && !strings.HasPrefix(s, "cmd://") {
			_ = ResolveSilent(s)
		}
		_ = ResolveSilent("op://" + s)
		_ = ResolveSilent("literal://" + s)
		if ResolveSilent("secret://loop") != "" {
			t.Fatalf("secret:// loop was resolved despite one-level indirection cap")
		}

		resolver := NewResolverWithFunc(func(reference string) (string, error) {
			if strings.Contains(reference, "nested") {
				return "secret://second", nil
			}
			if strings.Contains(reference, "fail") {
				return "", errors.New("fake resolver failure")
			}
			return "resolved", nil
		})
		_, _ = resolver.Resolve(s)
		injected, err := resolver.Inject(s)
		if err == nil && RefRegex.FindString(injected) != "" {
			t.Fatalf("Inject(%q) succeeded with unresolved reference left in %q", s, injected)
		}
		if _, err := resolver.Inject("value: secret://nested"); err == nil {
			t.Fatalf("Inject allowed a second-level secret reference")
		}
	})
}
