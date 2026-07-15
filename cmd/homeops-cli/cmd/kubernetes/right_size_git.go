package kubernetes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"homeops-cli/internal/common"
	shareddiff "homeops-cli/internal/diff"
	"homeops-cli/internal/ui"
	"k8s.io/apimachinery/pkg/api/resource"
)

type rightSizeGitOptions struct {
	RepoRoot string
	Write    bool
}

type rightSizeGitResult struct {
	Namespace string
	Workload  string
	Container string
	File      string
	Action    string
	Reason    string
}

type rightSizeGitFile struct {
	path     string
	original []byte
	updated  []byte
}

var rightSizeGitRootFn = func(ctx context.Context) (string, error) {
	output, err := common.RunCommandWithContextOutput(ctx, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("find GitOps repository root: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func applyRightSizeReportToGit(ctx context.Context, report rightSizeReport, options rightSizeGitOptions, out io.Writer) error {
	root, err := resolveRightSizeGitRoot(ctx, options.RepoRoot)
	if err != nil {
		return err
	}

	files := map[string]*rightSizeGitFile{}
	results := make([]rightSizeGitResult, 0, len(report.Containers))
	for _, container := range report.Containers {
		if container.Verdict != rightSizeOver && container.Verdict != rightSizeUnder {
			continue
		}
		result := rightSizeGitResult{
			Namespace: container.Namespace,
			Workload:  container.Workload,
			Container: container.Container,
			Action:    "SKIP",
		}
		path, reason := findRightSizeHelmRelease(root, container.Namespace, container.Workload)
		if path == "" {
			result.Reason = reason
			results = append(results, result)
			continue
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			result.Reason = "cannot make HelmRelease path relative to repository root"
			results = append(results, result)
			continue
		}
		result.File = filepath.ToSlash(relative)

		file := files[path]
		if file == nil {
			content, readErr := os.ReadFile(path) // #nosec G304 -- path is constrained to the selected GitOps repository tree.
			if readErr != nil {
				result.Reason = "read HelmRelease: " + readErr.Error()
				results = append(results, result)
				continue
			}
			file = &rightSizeGitFile{path: path, original: content, updated: append([]byte(nil), content...)}
			files[path] = file
		}

		updated, fields, editErr := editRightSizeContainerResources(file.updated, container)
		if editErr != nil {
			result.Reason = editErr.Error()
			results = append(results, result)
			continue
		}
		file.updated = updated
		result.Action = "APPLY"
		result.Reason = "update " + strings.Join(fields, ", ")
		results = append(results, result)
	}

	changed := make([]*rightSizeGitFile, 0, len(files))
	for _, file := range files {
		if !bytes.Equal(file.original, file.updated) {
			changed = append(changed, file)
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].path < changed[j].path })
	for _, file := range changed {
		relative, _ := filepath.Rel(root, file.path)
		if _, err := fmt.Fprint(out, shareddiff.Unified(filepath.ToSlash(relative), file.original, file.updated)); err != nil {
			return err
		}
		if options.Write {
			info, statErr := os.Stat(file.path)
			if statErr != nil {
				return fmt.Errorf("stat %s: %w", relative, statErr)
			}
			if err := os.WriteFile(file.path, file.updated, info.Mode().Perm()); err != nil { // #nosec G304 -- path is constrained to the selected GitOps repository tree.
				return fmt.Errorf("write %s: %w", relative, err)
			}
		}
	}

	if err := writeRightSizeGitSummary(out, results, options.Write); err != nil {
		return err
	}
	return writeRightSizeGitCommands(out, root, changed, options.Write)
}

func resolveRightSizeGitRoot(ctx context.Context, configured string) (string, error) {
	root := strings.TrimSpace(configured)
	var err error
	if root == "" {
		root, err = rightSizeGitRootFn(ctx)
		if err != nil {
			return "", err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(filepath.Join(root, "kubernetes", "apps"))
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("repository root %s has no kubernetes/apps directory", root)
	}
	return filepath.Clean(root), nil
}

func findRightSizeHelmRelease(root, namespace, workload string) (string, string) {
	namespaceDir := filepath.Join(root, "kubernetes", "apps", namespace)
	direct := filepath.Join(namespaceDir, workload, "app", "helmrelease.yaml")
	if info, err := os.Stat(direct); err == nil && !info.IsDir() {
		matches, matchErr := helmReleaseMatchesWorkload(direct, workload)
		if matchErr == nil && matches {
			return direct, ""
		}
	}

	pattern := filepath.Join(namespaceDir, "*", "app", "helmrelease.yaml")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return "", "scan HelmReleases: " + err.Error()
	}
	var matches []string
	for _, path := range paths {
		matched, matchErr := helmReleaseMatchesWorkload(path, workload)
		if matchErr == nil && matched {
			matches = append(matches, path)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Sprintf("no HelmRelease named %s found in namespace GitOps tree", workload)
	case 1:
		return matches[0], ""
	default:
		return "", fmt.Sprintf("ambiguous HelmRelease match for %s (%d files)", workload, len(matches))
	}
}

func helmReleaseMatchesWorkload(path, workload string) (bool, error) {
	content, err := os.ReadFile(path) // #nosec G304 -- caller supplies paths discovered beneath kubernetes/apps.
	if err != nil {
		return false, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	matches := 0
	for {
		var document struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return false, err
		}
		if document.Kind == "HelmRelease" && document.Metadata.Name == workload {
			matches++
		}
	}
	return matches == 1, nil
}

type rightSizeYAMLKey struct {
	indent int
	key    string
}

func editRightSizeContainerResources(content []byte, container rightSizeContainer) ([]byte, []string, error) {
	lines := splitRightSizeLines(content)
	fieldLines := map[string][]int{}
	stack := []rightSizeYAMLKey{}
	for index, line := range lines {
		indent, key, ok := parseRightSizeYAMLKey(line)
		if !ok {
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		path := make([]string, 0, len(stack)+1)
		for _, parent := range stack {
			path = append(path, parent.key)
		}
		path = append(path, key)
		if field, matched := rightSizeResourceField(path, container.Container); matched {
			fieldLines[field] = append(fieldLines[field], index)
		}
		stack = append(stack, rightSizeYAMLKey{indent: indent, key: key})
	}

	for field, indexes := range fieldLines {
		if len(indexes) > 1 {
			return nil, nil, fmt.Errorf("container resources.%s is ambiguous", field)
		}
	}
	requestFields := []struct {
		name  string
		value string
	}{
		{name: "requests.cpu", value: formatCPU(container.SuggestedCPUCores)},
		{name: "requests.memory", value: formatMemory(container.SuggestedMemoryBytes)},
	}
	var changed []string
	for _, field := range requestFields {
		indexes := fieldLines[field.name]
		if len(indexes) == 0 || field.value == "-" {
			continue
		}
		updated, err := replaceRightSizeYAMLScalar(lines[indexes[0]], field.value)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot edit %s: %w", field.name, err)
		}
		if updated != lines[indexes[0]] {
			lines[indexes[0]] = updated
			changed = append(changed, field.name+"="+field.value)
		}
	}

	nearLimit := container.Verdict == rightSizeUnder && container.MemoryLimitBytes > 0 &&
		container.MemoryMaxBytes >= 0.9*container.MemoryLimitBytes
	if nearLimit && len(fieldLines["limits.memory"]) == 1 {
		limit := roundMemoryRequest(container.MemoryMaxBytes * 1.25)
		if limit > container.MemoryLimitBytes {
			value := formatMemory(limit)
			index := fieldLines["limits.memory"][0]
			updated, err := replaceRightSizeYAMLScalar(lines[index], value)
			if err != nil {
				return nil, nil, fmt.Errorf("cannot edit limits.memory: %w", err)
			}
			if updated != lines[index] {
				lines[index] = updated
				changed = append(changed, "limits.memory="+value)
			}
		}
	}
	if len(changed) == 0 {
		return nil, nil, fmt.Errorf("no existing cpu or memory request lines found for container %s", container.Container)
	}
	return []byte(strings.Join(lines, "")), changed, nil
}

func rightSizeResourceField(path []string, container string) (string, bool) {
	if len(path) < 7 {
		return "", false
	}
	for i := 0; i+6 < len(path); i++ {
		if path[i] != "controllers" || path[i+2] != "containers" || path[i+3] != container || path[i+4] != "resources" {
			continue
		}
		section, resourceName := path[i+5], path[i+6]
		if (section == "requests" || section == "limits") && (resourceName == "cpu" || resourceName == "memory") {
			return section + "." + resourceName, true
		}
	}
	return "", false
}

func parseRightSizeYAMLKey(line string) (int, string, bool) {
	trimmedLine := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	trimmed := strings.TrimLeft(trimmedLine, " ")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") || strings.Contains(trimmedLine[:len(trimmedLine)-len(trimmed)], "\t") {
		return 0, "", false
	}
	colon := strings.IndexByte(trimmed, ':')
	if colon <= 0 {
		return 0, "", false
	}
	key := strings.TrimSpace(trimmed[:colon])
	if strings.ContainsAny(key, "{}[],&*!|>@`#") {
		return 0, "", false
	}
	key = strings.Trim(key, "\"'")
	if key == "" {
		return 0, "", false
	}
	return len(trimmedLine) - len(trimmed), key, true
}

func replaceRightSizeYAMLScalar(line, value string) (string, error) {
	newline := ""
	body := line
	if strings.HasSuffix(body, "\n") {
		newline = "\n"
		body = strings.TrimSuffix(body, "\n")
		if strings.HasSuffix(body, "\r") {
			body = strings.TrimSuffix(body, "\r")
			newline = "\r\n"
		}
	}
	colon := strings.IndexByte(body, ':')
	if colon < 0 {
		return "", fmt.Errorf("missing mapping colon")
	}
	prefix := body[:colon+1]
	rest := body[colon+1:]
	spaceCount := len(rest) - len(strings.TrimLeft(rest, " "))
	spacing := rest[:spaceCount]
	rest = rest[spaceCount:]
	if rest == "" || strings.HasPrefix(rest, "#") {
		return "", fmt.Errorf("missing scalar value")
	}
	comment := ""
	if index := strings.Index(rest, " #"); index >= 0 {
		comment = rest[index:]
		rest = rest[:index]
	}
	rest = strings.TrimSpace(rest)
	anchor := ""
	if strings.HasPrefix(rest, "&") {
		parts := strings.Fields(rest)
		if len(parts) != 2 {
			return "", fmt.Errorf("unsupported anchored scalar")
		}
		anchor = parts[0] + " "
		rest = parts[1]
	}
	if strings.ContainsAny(rest, " []{}*,") {
		return "", fmt.Errorf("unsupported scalar %q", rest)
	}
	scalar := rest
	quote := ""
	if unquoted, err := strconv.Unquote(rest); err == nil {
		scalar = unquoted
		quote = "\""
	} else if len(rest) >= 2 && strings.HasPrefix(rest, "'") && strings.HasSuffix(rest, "'") {
		scalar = strings.TrimSuffix(strings.TrimPrefix(rest, "'"), "'")
		quote = "'"
	}
	if _, err := resource.ParseQuantity(scalar); err != nil {
		return "", fmt.Errorf("existing value %q is not a resource quantity", scalar)
	}
	return prefix + spacing + anchor + quote + value + quote + comment + newline, nil
}

func splitRightSizeLines(content []byte) []string {
	var lines []string
	for len(content) > 0 {
		index := bytes.IndexByte(content, '\n')
		if index < 0 {
			lines = append(lines, string(content))
			break
		}
		lines = append(lines, string(content[:index+1]))
		content = content[index+1:]
	}
	return lines
}

func writeRightSizeGitSummary(out io.Writer, results []rightSizeGitResult, wrote bool) error {
	rows := make([][]string, 0, len(results))
	for _, result := range results {
		rows = append(rows, []string{result.Action, result.Namespace, result.Workload, result.Container, result.File, result.Reason})
	}
	mode := "PREVIEW"
	if wrote {
		mode = "WRITE"
	}
	_, err := fmt.Fprintf(out, "\n%s SUMMARY\n%s\n", mode,
		ui.Table([]string{"ACTION", "NAMESPACE", "WORKLOAD", "CONTAINER", "FILE", "REASON"}, rows))
	return err
}

func writeRightSizeGitCommands(out io.Writer, root string, files []*rightSizeGitFile, wrote bool) error {
	if len(files) == 0 {
		_, err := fmt.Fprintln(out, "\nNo HelmRelease changes to review.")
		return err
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		relative, _ := filepath.Rel(root, file.path)
		paths = append(paths, common.ShellQuote(filepath.ToSlash(relative)))
	}
	if !wrote {
		if _, err := fmt.Fprintln(out, "\nPreview only; rerun with --write to apply these edits."); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "\nReview and commit manually (this command never commits):\n  git -C %s diff -- %s\n  git -C %s add -- %s\n  git -C %s commit -m %s\n",
		common.ShellQuote(root), strings.Join(paths, " "), common.ShellQuote(root), strings.Join(paths, " "), common.ShellQuote(root), common.ShellQuote("chore: right-size Kubernetes resources"))
	return err
}
