// Package secrets resolves secret references through pluggable backends.
//
// A secret reference is a URI whose scheme selects the backend:
//
//	op://vault/item/field    1Password CLI (`op read`)
//	env://VAR_NAME           environment variable
//	file:///path/to/file     file contents (trailing newline trimmed, ~ expanded)
//	cmd://command args       stdout of a command run via the shell
//	literal://value          the value itself (for non-sensitive config knobs)
//	secret://key             indirection through the homeops config `secrets:` map
//
// Templates and config files use these references instead of hardcoding any
// particular secret manager, so the CLI works the same whether secrets live in
// 1Password, environment variables, files on disk, or an external tool.
package secrets

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Resolve resolves a single secret reference via its scheme's provider.
// References without a known scheme are rejected so typos fail loudly.
func Resolve(reference string) (string, error) {
	scheme, rest, ok := splitScheme(reference)
	if !ok {
		return "", fmt.Errorf("secret reference %q has no recognised scheme (expected one of: %s)", reference, knownSchemeList())
	}
	switch scheme {
	case "op":
		return resolveOp(reference)
	case "env":
		return resolveEnv(rest)
	case "file":
		return resolveFile(rest)
	case "cmd":
		return resolveCmd(rest)
	case "literal":
		return rest, nil
	case "secret":
		return resolveIndirect(rest)
	}
	return "", fmt.Errorf("secret reference %q has no recognised scheme (expected one of: %s)", reference, knownSchemeList())
}

// ResolveSilent resolves a reference, returning "" on any failure. Used on
// best-effort paths where a missing secret has a sane fallback (e.g. kubeadm
// minting a fresh CA when no persisted PKI exists).
func ResolveSilent(reference string) string {
	value, err := Resolve(reference)
	if err != nil {
		return ""
	}
	return value
}

// ResolveBatch resolves references in parallel. Failed lookups are omitted
// from the returned map.
func ResolveBatch(references []string) map[string]string {
	results := make(map[string]string, len(references))
	if len(references) == 0 {
		return results
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	resolver := NewResolver()
	for _, ref := range references {
		wg.Add(1)
		go func(reference string) {
			defer wg.Done()
			value, err := resolver.Resolve(reference)
			if err == nil && value != "" {
				mu.Lock()
				results[reference] = value
				mu.Unlock()
			}
		}(ref)
	}
	wg.Wait()
	return results
}

// keymapFn maps a `secret://<key>` indirection to its configured backing
// reference. Registered by the config package at load time so this package
// never imports it.
var (
	keymapMu sync.RWMutex
	keymapFn func(key string) (string, bool)
)

// RegisterKeymap installs the lookup used by secret:// references. The config
// package calls this once after loading the homeops config file.
func RegisterKeymap(fn func(key string) (string, bool)) {
	keymapMu.Lock()
	defer keymapMu.Unlock()
	keymapFn = fn
}

func resolveIndirect(key string) (string, error) {
	keymapMu.RLock()
	fn := keymapFn
	keymapMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("secret key %q referenced but no homeops config is loaded (run 'homeops-cli config init' to create one)", key)
	}
	backing, ok := fn(key)
	if !ok {
		return "", fmt.Errorf("secret key %q is not defined in the homeops config 'secrets:' map (add it, or run 'homeops-cli config doctor')", key)
	}
	if strings.HasPrefix(backing, "secret://") {
		return "", fmt.Errorf("secret key %q maps to another secret:// reference (%s) — only one level of indirection is allowed", key, backing)
	}
	return Resolve(backing)
}

func splitScheme(reference string) (scheme, rest string, ok bool) {
	idx := strings.Index(reference, "://")
	if idx <= 0 {
		return "", "", false
	}
	scheme = reference[:idx]
	if !isKnownScheme(scheme) {
		return "", "", false
	}
	return scheme, reference[idx+3:], true
}

var knownSchemes = []string{"op", "env", "file", "cmd", "literal", "secret"}

func isKnownScheme(s string) bool {
	for _, k := range knownSchemes {
		if s == k {
			return true
		}
	}
	return false
}

func knownSchemeList() string {
	out := make([]string, len(knownSchemes))
	for i, s := range knownSchemes {
		out[i] = s + "://"
	}
	return strings.Join(out, ", ")
}

// IsReference reports whether s looks like a resolvable secret reference.
func IsReference(s string) bool {
	_, _, ok := splitScheme(s)
	return ok
}

// RefRegex matches inline secret references inside rendered content. cmd://
// and literal:// are deliberately excluded from inline injection: command
// lines contain spaces (unmatchable inline) and running commands found inside
// template content would be a template-injection hazard.
var RefRegex = regexp.MustCompile(`(op|env|file|secret)://[^\s"']+`)

// OpRefRegex matches 1Password references of the form op://vault/item/field.
// Kept for callers that specifically need to detect op:// usage (e.g. to know
// whether the `op` CLI is required at all).
var OpRefRegex = regexp.MustCompile(`op://([^/]+)/([^/]+)/([^\s"']+)`)

type resolution struct {
	value string
	err   error
	ready chan struct{}
}

// Resolver caches reference lookups for the lifetime of a single operation.
// Values stay in memory only and are never persisted by the resolver.
type Resolver struct {
	mu      sync.Mutex
	resolve func(reference string) (string, error)
	cache   map[string]*resolution
}

// NewResolver creates an operation-scoped caching resolver backed by the
// scheme providers.
func NewResolver() *Resolver {
	return NewResolverWithFunc(Resolve)
}

// NewResolverWithFunc creates a caching resolver with a custom resolve
// function (used by tests).
func NewResolverWithFunc(fn func(reference string) (string, error)) *Resolver {
	if fn == nil {
		fn = Resolve
	}
	return &Resolver{
		resolve: fn,
		cache:   make(map[string]*resolution),
	}
}

// Resolve returns the value for a reference, caching successes and failures.
func (r *Resolver) Resolve(reference string) (string, error) {
	if r == nil {
		return Resolve(reference)
	}

	r.mu.Lock()
	if r.cache == nil {
		r.cache = make(map[string]*resolution)
	}
	if cached, ok := r.cache[reference]; ok {
		r.mu.Unlock()
		<-cached.ready
		return cached.value, cached.err
	}
	fn := r.resolve
	if fn == nil {
		fn = Resolve
	}
	res := &resolution{ready: make(chan struct{})}
	r.cache[reference] = res
	r.mu.Unlock()

	value, err := fn(reference)

	r.mu.Lock()
	res.value = value
	res.err = err
	close(res.ready)
	r.mu.Unlock()

	return value, err
}

// Inject replaces inline secret references in content with their resolved
// values. All failures are accumulated and reported together so the user sees
// every unresolvable reference at once.
func (r *Resolver) Inject(content string) (string, error) {
	refs := RefRegex.FindAllString(content, -1)
	if len(refs) == 0 {
		return content, nil
	}

	seen := make(map[string]struct{}, len(refs))
	orderedRefs := make([]string, 0, len(refs))
	errCache := make(map[string]error)
	for _, ref := range refs {
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		orderedRefs = append(orderedRefs, ref)
		if _, err := r.Resolve(ref); err != nil {
			errCache[ref] = err
		}
	}

	if len(errCache) > 0 {
		var b strings.Builder
		b.WriteString("failed to resolve secret references:\n")
		for _, ref := range orderedRefs {
			err, ok := errCache[ref]
			if !ok {
				continue
			}
			b.WriteString(" - ")
			b.WriteString(ref)
			b.WriteString(": ")
			b.WriteString(err.Error())
			b.WriteByte('\n')
		}
		msg := strings.TrimRight(b.String(), "\n")
		return "", fmt.Errorf("%s", msg)
	}

	result := RefRegex.ReplaceAllStringFunc(content, func(fullMatch string) string {
		value, err := r.Resolve(fullMatch)
		if err != nil {
			return fullMatch
		}
		return value
	})

	if rem := RefRegex.FindString(result); rem != "" {
		return "", fmt.Errorf("unresolved secret reference remains: %s", rem)
	}

	return result, nil
}

// Inject replaces inline secret references in content using a fresh resolver.
func Inject(content string) (string, error) {
	return NewResolver().Inject(content)
}

// ListReferences returns the deduplicated, sorted inline references found in
// content. Used by `config doctor` to validate template resolvability.
func ListReferences(content string) []string {
	refs := RefRegex.FindAllString(content, -1)
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}
