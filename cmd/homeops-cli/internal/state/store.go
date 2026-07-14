// Package state persists cluster state that must outlive nuke/pave cycles:
// the admin kubeconfig and the kubeadm cluster PKI. Two backends exist —
// "op" (a 1Password item) and "file" (local paths, 0600) — selected via the
// state: section of the homeops config.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/secrets"
)

// KubeconfigStore persists the admin kubeconfig.
type KubeconfigStore interface {
	// Save persists kubeconfig content.
	Save(content []byte, logger *common.ColorLogger) error
	// Pull writes the persisted kubeconfig to destPath (0600).
	Pull(destPath string, logger *common.ColorLogger) error
	// Describe names the backing location for log/UX messages.
	Describe() string
}

// PKIStore persists the kubeadm cluster PKI (base64 field -> content).
type PKIStore interface {
	// GetField returns a persisted field value, or "" when absent (callers
	// treat a miss as "no persisted PKI; mint fresh").
	GetField(field string) string
	// Save replaces the persisted PKI with fields.
	Save(fields map[string]string) error
	// Describe names the backing location for log/UX messages.
	Describe() string
}

// NewKubeconfigStore builds the kubeconfig store from configuration.
func NewKubeconfigStore(cfg config.StoreConfig) KubeconfigStore {
	if cfg.Backend == "op" {
		return &opKubeconfigStore{loc: cfg.Op}
	}
	return &fileKubeconfigStore{path: cfg.Path}
}

// NewPKIStore builds the PKI store from configuration.
func NewPKIStore(cfg config.StoreConfig) PKIStore {
	if cfg.Backend == "op" {
		return &opPKIStore{loc: cfg.Op}
	}
	return &filePKIStore{dir: cfg.Path}
}

// ---------------------------------------------------------------------------
// file backend

type fileKubeconfigStore struct{ path string }

func (s *fileKubeconfigStore) Describe() string { return fmt.Sprintf("file %s", s.path) }

func (s *fileKubeconfigStore) Save(content []byte, logger *common.ColorLogger) error {
	path, err := secrets.ExpandHome(s.path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create state directory %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		return fmt.Errorf("failed to write kubeconfig state to %s: %w", path, err)
	}
	logger.Debug("Kubeconfig saved to %s", path)
	return nil
}

func (s *fileKubeconfigStore) Pull(destPath string, logger *common.ColorLogger) error {
	path, err := secrets.ExpandHome(s.path)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(path) // #nosec G304 -- kubeconfig state path is explicitly configured by the local operator
	if err != nil {
		return fmt.Errorf("no persisted kubeconfig at %s (bootstrap saves one on success): %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}
	if err := os.WriteFile(destPath, content, 0600); err != nil { // #nosec G703 -- kubeconfig destination is an explicit local CLI output path
		return fmt.Errorf("failed to write kubeconfig to %s: %w", destPath, err)
	}
	logger.Debug("Kubeconfig pulled from %s to %s", path, destPath)
	return nil
}

type filePKIStore struct{ dir string }

func (s *filePKIStore) Describe() string { return fmt.Sprintf("directory %s", s.dir) }

func (s *filePKIStore) GetField(field string) string {
	dir, err := secrets.ExpandHome(s.dir)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, field)) // #nosec G304 -- PKI field file is read from explicitly configured local state directory
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s *filePKIStore) Save(fields map[string]string) error {
	dir, err := secrets.ExpandHome(s.dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create PKI state directory %s: %w", dir, err)
	}
	for field, value := range fields {
		path := filepath.Join(dir, field)
		if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
			return fmt.Errorf("failed to write PKI field %s: %w", path, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// op backend

// runOpFn runs the op CLI with NO stdin so op never tries to read a JSON
// template from a pipe (the trap that bites interactive/over-ssh op
// invocations). Swappable for tests.
var runOpFn = func(args ...string) error {
	c := common.Command("op", args...)
	c.Stdin = nil
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("op %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runOpStdinFn runs op with stdin piped in (an item template), so secret field
// values travel via stdin and never appear in argv / /proc/<pid>/cmdline.
// Swappable for tests.
var runOpStdinFn = func(stdin []byte, args ...string) error {
	c := common.Command("op", args...)
	c.Stdin = strings.NewReader(string(stdin))
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("op %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetRunOpFnsForTesting overrides the op executors for the duration of a test.
func SetRunOpFnsForTesting(run func(args ...string) error, runStdin func(stdin []byte, args ...string) error) func() {
	oldRun, oldStdin := runOpFn, runOpStdinFn
	if run != nil {
		runOpFn = run
	}
	if runStdin != nil {
		runOpStdinFn = runStdin
	}
	return func() { runOpFn, runOpStdinFn = oldRun, oldStdin }
}

// filterConnectEnvVars removes OP_CONNECT_HOST and OP_CONNECT_TOKEN from env
// vars because "op item edit" doesn't work with 1Password Connect.
func filterConnectEnvVars(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "OP_CONNECT_HOST=") && !strings.HasPrefix(e, "OP_CONNECT_TOKEN=") {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

type opKubeconfigStore struct{ loc config.OpLocation }

func (s *opKubeconfigStore) Describe() string {
	return fmt.Sprintf("1Password op://%s/%s/%s", s.loc.Vault, s.loc.Item, s.loc.Field)
}

func (s *opKubeconfigStore) Save(content []byte, logger *common.ColorLogger) error {
	logger.Debug("Updating kubeconfig file in 1Password...")

	tmpFile, err := os.CreateTemp("", "kubeconfig-*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temporary kubeconfig file: %w", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	if _, err := tmpFile.Write(content); err != nil {
		return fmt.Errorf("failed to write kubeconfig to temporary file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}

	filteredEnv := filterConnectEnvVars(os.Environ())

	// Delete any existing kubeconfig file attachment to avoid duplicates,
	// then re-add it with the new file. Field-might-not-exist errors are fine.
	deleteCmd := common.Command("op", "item", "edit", s.loc.Item, "--vault", s.loc.Vault,
		fmt.Sprintf("%s[delete]", s.loc.Field))
	deleteCmd.Env = filteredEnv
	_ = deleteCmd.Run()

	cmd := common.Command("op", "item", "edit", s.loc.Item, "--vault", s.loc.Vault,
		fmt.Sprintf("%s[file]=%s", s.loc.Field, tmpFile.Name()))
	cmd.Env = filteredEnv
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to update kubeconfig file in 1Password: %w (output: %s)", err, common.RedactCommandOutput(string(output)))
	}

	logger.Debug("Kubeconfig file updated in 1Password")
	return nil
}

func (s *opKubeconfigStore) Pull(destPath string, logger *common.ColorLogger) error {
	logger.Debug("Pulling kubeconfig from 1Password...")

	if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	fileID, err := s.fileID()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig file ID: %w", err)
	}

	// Use op read with the file ID to avoid ambiguity between duplicates.
	ref := fmt.Sprintf("op://%s/%s/%s", s.loc.Vault, s.loc.Item, fileID)
	cmd := common.Command("op", "read", ref, "--out-file", destPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull kubeconfig from 1Password: %w (output: %s)", err, common.RedactCommandOutput(string(output)))
	}

	if err := os.Chmod(destPath, 0600); err != nil {
		logger.Warn("Failed to set kubeconfig permissions: %v", err)
	}

	logger.Debug("Kubeconfig pulled from 1Password to %s", destPath)
	return nil
}

// fileID retrieves the file ID for the kubeconfig attachment.
func (s *opKubeconfigStore) fileID() (string, error) {
	cmd := common.Command("op", "item", "get", s.loc.Item, "--vault", s.loc.Vault, "--format=json")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get item: %w", err)
	}

	var item struct {
		Files []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(output, &item); err != nil {
		return "", fmt.Errorf("failed to parse item: %w", err)
	}

	for _, f := range item.Files {
		if f.Name == s.loc.Field {
			return f.ID, nil
		}
	}

	return "", fmt.Errorf("no kubeconfig file found in item")
}

type opPKIStore struct{ loc config.OpLocation }

func (s *opPKIStore) Describe() string {
	return fmt.Sprintf("1Password op://%s/%s", s.loc.Vault, s.loc.Item)
}

func (s *opPKIStore) GetField(field string) string {
	return secrets.ResolveSilent(fmt.Sprintf("op://%s/%s/%s", s.loc.Vault, s.loc.Item, field))
}

type opField struct {
	ID      string `json:"id,omitempty"`
	Label   string `json:"label,omitempty"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
	Value   string `json:"value"`
}

type opItemTemplate struct {
	Title    string    `json:"title"`
	Category string    `json:"category"`
	Fields   []opField `json:"fields"`
}

// buildPKITemplate turns captured PKI (field -> base64) into an op item
// template. *_key fields are CONCEALED. Field order is deterministic.
func (s *opPKIStore) buildPKITemplate(fields map[string]string) opItemTemplate {
	t := opItemTemplate{
		Title:    s.loc.Item,
		Category: "SECURE_NOTE",
		Fields: []opField{{
			ID: "notesPlain", Type: "STRING", Purpose: "NOTES",
			Value: "kubeadm cluster PKI (base64). Restored by homeops-cli before kubeadm init for a stable cluster identity across rebuilds. Managed by 'flatcar save-pki'.",
		}},
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		typ := "STRING"
		if strings.HasSuffix(k, "_key") {
			typ = "CONCEALED"
		}
		t.Fields = append(t.Fields, opField{Label: k, Type: typ, Value: fields[k]})
	}
	return t
}

// Save persists captured PKI to the configured 1Password item, replacing any
// existing one. The base64 CA/SA/etcd PRIVATE keys are passed via an item
// template on STDIN (never argv), so they don't appear in /proc/<pid>/cmdline.
func (s *opPKIStore) Save(fields map[string]string) error {
	_ = runOpFn("item", "delete", s.loc.Item, "--vault", s.loc.Vault) // ignore if absent
	doc, err := json.Marshal(s.buildPKITemplate(fields))
	if err != nil {
		return fmt.Errorf("marshal op item template: %w", err)
	}
	return runOpStdinFn(doc, "item", "create", "--vault", s.loc.Vault)
}
