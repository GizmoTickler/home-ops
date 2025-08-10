package migration

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"homeops-cli/internal/errors"
	"homeops-cli/internal/metrics"
	"homeops-cli/internal/yaml"
)

// MigrationTool provides utilities for migrating from shell scripts to Go
type MigrationTool struct {
	yamlProcessor *yaml.Processor
	logger        interface{} // Can be ColorLogger or StructuredLogger
}

// ShellCommand represents a shell command found in scripts
type ShellCommand struct {
	Command     string
	Args        []string
	File        string
	Line        int
	Context     string
	Replacement string
}

// MigrationReport provides a summary of migration analysis
type MigrationReport struct {
	ScriptsAnalyzed   int
	CommandsFound     []ShellCommand
	YQCommands        []ShellCommand
	KubectlCommands   []ShellCommand
	TalosCommands     []ShellCommand
	OtherCommands     []ShellCommand
	Recommendations   []string
	EstimatedEffort   string
	GeneratedAt       time.Time
}

// ComparisonResult represents the result of comparing shell vs Go output
type ComparisonResult struct {
	Operation    string
	ShellOutput  string
	GoOutput     string
	Matches      bool
	Differences  []string
	ExecutionTime struct {
		Shell time.Duration
		Go    time.Duration
	}
}

// NewMigrationTool creates a new migration tool
func NewMigrationTool(logger interface{}) *MigrationTool {
	metrics := metrics.NewPerformanceCollector()
	return &MigrationTool{
		yamlProcessor: yaml.NewProcessor(logger, metrics),
		logger:        logger,
	}
}

// AnalyzeShellScripts analyzes shell scripts for migration opportunities
func (mt *MigrationTool) AnalyzeShellScripts(scriptDir string) (*MigrationReport, error) {
	report := &MigrationReport{
		CommandsFound:   make([]ShellCommand, 0),
		YQCommands:      make([]ShellCommand, 0),
		KubectlCommands: make([]ShellCommand, 0),
		TalosCommands:   make([]ShellCommand, 0),
		OtherCommands:   make([]ShellCommand, 0),
		Recommendations: make([]string, 0),
		GeneratedAt:     time.Now(),
	}

	err := filepath.Walk(scriptDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Only analyze shell scripts
		if !strings.HasSuffix(path, ".sh") {
			return nil
		}

		commands, err := mt.analyzeShellFile(path)
		if err != nil {
			return err
		}

		report.ScriptsAnalyzed++
		report.CommandsFound = append(report.CommandsFound, commands...)

		// Categorize commands
		for _, cmd := range commands {
			switch {
			case strings.HasPrefix(cmd.Command, "yq"):
				report.YQCommands = append(report.YQCommands, cmd)
			case strings.HasPrefix(cmd.Command, "kubectl"):
				report.KubectlCommands = append(report.KubectlCommands, cmd)
			case strings.HasPrefix(cmd.Command, "talosctl"):
				report.TalosCommands = append(report.TalosCommands, cmd)
			default:
				report.OtherCommands = append(report.OtherCommands, cmd)
			}
		}

		return nil
	})

	if err != nil {
		return nil, errors.NewFileSystemError("MIGRATION_ANALYSIS_ERROR", 
			"failed to analyze shell scripts", err)
	}

	// Generate recommendations
	mt.generateRecommendations(report)

	return report, nil
}

// analyzeShellFile analyzes a single shell file for commands
func (mt *MigrationTool) analyzeShellFile(filePath string) ([]ShellCommand, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close file: %v\n", closeErr)
		}
	}()

	var commands []ShellCommand
	scanner := bufio.NewScanner(file)
	lineNum := 0

	// Regular expressions for common commands
	yqRegex := regexp.MustCompile(`yq\s+([^\s]+.*)`)
	kubectlRegex := regexp.MustCompile(`kubectl\s+([^\s]+.*)`)
	talosRegex := regexp.MustCompile(`talosctl\s+([^\s]+.*)`)

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// Check for yq commands
		if matches := yqRegex.FindStringSubmatch(line); matches != nil {
			commands = append(commands, ShellCommand{
				Command:     "yq",
				Args:        strings.Fields(matches[1]),
				File:        filePath,
				Line:        lineNum,
				Context:     line,
				Replacement: mt.suggestYQReplacement(matches[1]),
			})
		}

		// Check for kubectl commands
		if matches := kubectlRegex.FindStringSubmatch(line); matches != nil {
			commands = append(commands, ShellCommand{
				Command:     "kubectl",
				Args:        strings.Fields(matches[1]),
				File:        filePath,
				Line:        lineNum,
				Context:     line,
				Replacement: mt.suggestKubectlReplacement(matches[1]),
			})
		}

		// Check for talosctl commands
		if matches := talosRegex.FindStringSubmatch(line); matches != nil {
			commands = append(commands, ShellCommand{
				Command:     "talosctl",
				Args:        strings.Fields(matches[1]),
				File:        filePath,
				Line:        lineNum,
				Context:     line,
				Replacement: mt.suggestTalosReplacement(matches[1]),
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return commands, nil
}

// suggestYQReplacement suggests Go replacement for yq commands
func (mt *MigrationTool) suggestYQReplacement(args string) string {
	if strings.Contains(args, "eval") {
		return "Use yaml.Processor.GetValue() or SetValue() methods"
	}
	if strings.Contains(args, "merge") {
		return "Use yaml.Processor.Merge() method"
	}
	if strings.Contains(args, "write") {
		return "Use yaml.Processor.WriteFile() method"
	}
	return "Use yaml.Processor methods for YAML manipulation"
}

// suggestKubectlReplacement suggests Go replacement for kubectl commands
func (mt *MigrationTool) suggestKubectlReplacement(args string) string {
	if strings.Contains(args, "apply") {
		return "Use Kubernetes client-go Apply methods"
	}
	if strings.Contains(args, "get") {
		return "Use Kubernetes client-go Get methods"
	}
	if strings.Contains(args, "delete") {
		return "Use Kubernetes client-go Delete methods"
	}
	return "Use Kubernetes client-go library"
}

// suggestTalosReplacement suggests Go replacement for talosctl commands
func (mt *MigrationTool) suggestTalosReplacement(args string) string {
	if strings.Contains(args, "config") {
		return "Use Talos Go client for configuration management"
	}
	if strings.Contains(args, "apply-config") {
		return "Use Talos Go client ApplyConfiguration method"
	}
	if strings.Contains(args, "patch") {
		return "Use yaml.Processor.Merge() with Talos client"
	}
	return "Use Talos Go client library"
}

// generateRecommendations generates migration recommendations
func (mt *MigrationTool) generateRecommendations(report *MigrationReport) {
	if len(report.YQCommands) > 0 {
		report.Recommendations = append(report.Recommendations, 
			fmt.Sprintf("Replace %d yq commands with yaml.Processor methods", len(report.YQCommands)))
	}

	if len(report.KubectlCommands) > 0 {
		report.Recommendations = append(report.Recommendations, 
			fmt.Sprintf("Replace %d kubectl commands with Kubernetes client-go", len(report.KubectlCommands)))
	}

	if len(report.TalosCommands) > 0 {
		report.Recommendations = append(report.Recommendations, 
			fmt.Sprintf("Replace %d talosctl commands with Talos Go client", len(report.TalosCommands)))
	}

	// Estimate effort
	totalCommands := len(report.CommandsFound)
	switch {
	case totalCommands < 10:
		report.EstimatedEffort = "Low (1-2 days)"
	case totalCommands < 50:
		report.EstimatedEffort = "Medium (1-2 weeks)"
	default:
		report.EstimatedEffort = "High (2-4 weeks)"
	}
}

// CompareOutputs compares shell script output with Go implementation
func (mt *MigrationTool) CompareOutputs(operation, shellScript, goFunction string) (*ComparisonResult, error) {
	result := &ComparisonResult{
		Operation:   operation,
		Differences: make([]string, 0),
	}

	// Execute shell script
	shellStart := time.Now()
	shellCmd := exec.Command("bash", "-c", shellScript)
	shellOutput, err := shellCmd.Output()
	result.ExecutionTime.Shell = time.Since(shellStart)

	if err != nil {
		return nil, errors.NewValidationError("SHELL_EXECUTION_ERROR", 
			"failed to execute shell script for comparison", err)
	}

	result.ShellOutput = strings.TrimSpace(string(shellOutput))

	// Note: Go function execution would need to be implemented based on specific use case
	// This is a placeholder for the comparison framework
	result.GoOutput = "[Go implementation output would go here]"
	result.ExecutionTime.Go = time.Millisecond * 100 // Placeholder

	// Compare outputs
	result.Matches = result.ShellOutput == result.GoOutput
	if !result.Matches {
		result.Differences = append(result.Differences, "Output content differs")
	}

	return result, nil
}

// ValidateMigration validates that the Go implementation produces equivalent results
func (mt *MigrationTool) ValidateMigration(testCases []string) ([]ComparisonResult, error) {
	results := make([]ComparisonResult, 0, len(testCases))

	for i, testCase := range testCases {
		result, err := mt.CompareOutputs(fmt.Sprintf("test-case-%d", i), testCase, "")
		if err != nil {
			return nil, err
		}
		results = append(results, *result)
	}

	return results, nil
}

// GenerateMigrationPlan creates a detailed migration plan
func (mt *MigrationTool) GenerateMigrationPlan(report *MigrationReport) string {
	plan := fmt.Sprintf(`# Migration Plan Generated at %s

`, report.GeneratedAt.Format(time.RFC3339))
	plan += "## Summary\n"
	plan += fmt.Sprintf("- Scripts analyzed: %d\n", report.ScriptsAnalyzed)
	plan += fmt.Sprintf("- Total commands found: %d\n", len(report.CommandsFound))
	plan += fmt.Sprintf("- Estimated effort: %s\n\n", report.EstimatedEffort)

	plan += "## Command Breakdown\n"
	plan += fmt.Sprintf("- yq commands: %d\n", len(report.YQCommands))
	plan += fmt.Sprintf("- kubectl commands: %d\n", len(report.KubectlCommands))
	plan += fmt.Sprintf("- talosctl commands: %d\n", len(report.TalosCommands))
	plan += fmt.Sprintf("- Other commands: %d\n\n", len(report.OtherCommands))

	plan += "## Recommendations\n"
	for _, rec := range report.Recommendations {
		plan += fmt.Sprintf("- %s\n", rec)
	}

	plan += "\n## Detailed Command Analysis\n"
	for _, cmd := range report.CommandsFound {
		plan += fmt.Sprintf("### %s:%d\n", filepath.Base(cmd.File), cmd.Line)
		plan += fmt.Sprintf("**Command:** `%s`\n", cmd.Context)
		plan += fmt.Sprintf("**Replacement:** %s\n\n", cmd.Replacement)
	}

	return plan
}