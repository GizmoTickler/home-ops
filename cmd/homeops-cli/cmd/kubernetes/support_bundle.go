package kubernetes

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/flatcar"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ui"
)

const supportBundleCollectorTimeout = time.Minute

type supportBundleCollector struct {
	Name     string
	Filename string
	SSH      bool
	Collect  func(context.Context) ([]byte, error)
}

type supportBundleCollectorResult struct {
	Name       string `json:"name"`
	File       string `json:"file,omitempty"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type supportBundleManifest struct {
	CreatedAt  string                         `json:"created_at"`
	CLIVersion string                         `json:"cli_version"`
	Versions   map[string]string              `json:"versions"`
	Contents   []string                       `json:"contents"`
	Collectors []supportBundleCollectorResult `json:"collectors"`
}

type supportBundleRunResult struct {
	Path       string
	Size       int64
	Collectors []supportBundleCollectorResult
}

type supportBundleCommandResult struct {
	Path       string                         `json:"path"`
	Size       int64                          `json:"size_bytes"`
	Collectors []supportBundleCollectorResult `json:"collectors"`
	Drift      *supportBundleDriftReport      `json:"drift,omitempty"`
}

type supportBundleFluxSummary struct {
	Kustomizations []fluxKustomizationSummary `json:"kustomizations"`
	HelmReleases   []fluxTreeHelmNode         `json:"helm_releases"`
}

var (
	supportBundleNowFn           = time.Now
	supportBundleKubectlOutputFn = func(ctx context.Context, args ...string) ([]byte, error) {
		return kubectlOutputCtxFn(ctx, args...)
	}
	supportBundleCollectorsFn = defaultSupportBundleCollectors
	supportBundleTempDirFn    = func() (string, error) { return os.MkdirTemp("", "homeops-support-bundle-*") }
	supportBundleArchiveFn    = writeSupportBundleArchive
)

var supportBundleForbiddenPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"kubeconfig key data", regexp.MustCompile(`(?im)^\s*client-key-data\s*:`)},
	{"secret-labeled value", regexp.MustCompile(`(?i)\b(?:access[_-]?token|refresh[_-]?token|client[_-]?secret|api[_-]?key|password|passwd|private[_-]?key|token)\s*[:=]\s*["']?[A-Za-z0-9+/_.=-]{6,}`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`)},
	{"kubeadm token", regexp.MustCompile(`\b[a-z0-9]{6}\.[a-z0-9]{16}\b`)},
	{"1Password reference", regexp.MustCompile(`\bop://[^\s"']+`)},
}

func newSupportBundleCommand() *cobra.Command {
	var outputPath string
	var diffPath string
	var noSSH bool
	var failOnDrift bool
	cmd := &cobra.Command{
		Use:          "support-bundle",
		Short:        "Create a redaction-checked diagnostic archive",
		SilenceUsage: true,
		Example: `  homeops-cli k8s support-bundle
  homeops-cli k8s support-bundle --no-ssh
  homeops-cli k8s support-bundle --output ./cluster-diagnostics.tar.gz
  homeops-cli k8s support-bundle --diff ./before.tar.gz
  homeops-cli k8s support-bundle --diff ./before.tar.gz --output json
  homeops-cli k8s support-bundle --diff ./before.tar.gz --fail-on-drift`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if failOnDrift && strings.TrimSpace(diffPath) == "" {
				return fmt.Errorf("--fail-on-drift requires --diff")
			}
			format := "table"
			archivePath := outputPath
			if diffPath != "" && strings.EqualFold(strings.TrimSpace(outputPath), "json") {
				format, archivePath = "json", ""
			}
			var oldBundle *supportBundleArchive
			if strings.TrimSpace(diffPath) != "" {
				validated, err := loadSupportBundleArchive(diffPath)
				if err != nil {
					return fmt.Errorf("validate old support bundle: %w", err)
				}
				oldBundle = &validated
			}
			result, err := runSupportBundle(cmd.Context(), archivePath, noSSH)
			if err != nil {
				return err
			}
			commandResult := supportBundleCommandResult{Path: result.Path, Size: result.Size, Collectors: result.Collectors}
			if oldBundle != nil {
				freshBundle, readErr := loadSupportBundleArchive(result.Path)
				if readErr != nil {
					return fmt.Errorf("read fresh support bundle: %w", readErr)
				}
				drift := compareSupportBundles(*oldBundle, freshBundle)
				commandResult.Drift = &drift
			}
			if format == "json" {
				raw, marshalErr := json.MarshalIndent(commandResult, "", "  ")
				if marshalErr != nil {
					return fmt.Errorf("marshal support bundle result: %w", marshalErr)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Support bundle: %s (%s)\n", result.Path, humanBytes(result.Size))
				if commandResult.Drift != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n%s\n\n", renderSupportBundleDrift(*commandResult.Drift))
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), renderSupportBundleStatuses(result.Collectors))
			}
			if failOnDrift && commandResult.Drift != nil && commandResult.Drift.Summary.NewFail > 0 {
				return fmt.Errorf("support bundle drift found %d new failing finding(s)", commandResult.Drift.Summary.NewFail)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "archive path; with --diff, use json for structured stdout and the default archive path")
	cmd.Flags().BoolVar(&noSSH, "no-ssh", false, "skip certificate and Flatcar OS collectors that require SSH")
	cmd.Flags().StringVar(&diffPath, "diff", "", "compare the fresh bundle with an earlier support bundle archive")
	cmd.Flags().BoolVar(&failOnDrift, "fail-on-drift", false, "return exit code 1 when drift contains any NEW-FAIL finding")
	return cmd
}

func runSupportBundle(ctx context.Context, outputPath string, noSSH bool) (supportBundleRunResult, error) {
	createdAt := supportBundleNowFn().UTC()
	if strings.TrimSpace(outputPath) == "" {
		outputPath = "homeops-support-bundle-" + createdAt.Format("20060102T150405Z") + ".tar.gz"
	}
	absolutePath, err := filepath.Abs(filepath.Clean(outputPath))
	if err != nil {
		return supportBundleRunResult{}, fmt.Errorf("resolve support bundle path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o750); err != nil {
		return supportBundleRunResult{}, fmt.Errorf("create support bundle output directory: %w", err)
	}
	tempDir, err := supportBundleTempDirFn()
	if err != nil {
		return supportBundleRunResult{}, fmt.Errorf("create support bundle staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	root, err := os.OpenRoot(tempDir)
	if err != nil {
		return supportBundleRunResult{}, fmt.Errorf("open support bundle staging directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	collectors := supportBundleCollectorsFn(noSSH)
	results := make([]supportBundleCollectorResult, 0, len(collectors))
	contents := make([]string, 0, len(collectors)+1)
	for _, collector := range collectors {
		if noSSH && collector.SSH {
			results = append(results, supportBundleCollectorResult{Name: collector.Name, Status: "SKIPPED"})
			continue
		}
		started := supportBundleNowFn()
		collectorCtx, cancel := context.WithTimeout(ctx, supportBundleCollectorTimeout)
		data, collectErr := runSupportBundleCollector(collectorCtx, collector)
		cancel()
		duration := supportBundleNowFn().Sub(started)
		result := supportBundleCollectorResult{Name: collector.Name, DurationMS: duration.Milliseconds()}
		filename := filepath.ToSlash(filepath.Clean(collector.Filename))
		if filename == "." || strings.HasPrefix(filename, "../") || filepath.IsAbs(filename) {
			return supportBundleRunResult{}, fmt.Errorf("collector %s has unsafe filename", collector.Name)
		}
		if collectErr == nil && strings.HasSuffix(filename, ".json") && !json.Valid(data) {
			collectErr = fmt.Errorf("collector returned invalid JSON")
		}
		if collectErr != nil {
			filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".error"
			message := common.RedactCommandOutput(collectErr.Error())
			if writeErr := root.WriteFile(filename, []byte(message+"\n"), 0o600); writeErr != nil {
				return supportBundleRunResult{}, fmt.Errorf("write collector error %s: %w", collector.Name, writeErr)
			}
			result.Status, result.Error = "ERROR", message
		} else {
			if writeErr := root.WriteFile(filename, data, 0o600); writeErr != nil {
				return supportBundleRunResult{}, fmt.Errorf("write collector %s: %w", collector.Name, writeErr)
			}
			result.Status = "OK"
		}
		result.File = filename
		contents = append(contents, filename)
		results = append(results, result)
	}
	sort.Strings(contents)
	contents = append(contents, "manifest.json")
	sort.Strings(contents)
	manifest := supportBundleManifest{
		CreatedAt: createdAt.Format(time.RFC3339), CLIVersion: supportBundleCLIVersion(), Versions: supportBundleVersions(root),
		Contents: contents, Collectors: results,
	}
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return supportBundleRunResult{}, fmt.Errorf("marshal support bundle manifest: %w", err)
	}
	if err := root.WriteFile("manifest.json", manifestRaw, 0o600); err != nil {
		return supportBundleRunResult{}, fmt.Errorf("write support bundle manifest: %w", err)
	}
	if err := scanSupportBundle(root, contents); err != nil {
		return supportBundleRunResult{}, err
	}
	if err := supportBundleArchiveFn(tempDir, absolutePath, contents); err != nil {
		return supportBundleRunResult{}, err
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return supportBundleRunResult{}, fmt.Errorf("stat support bundle: %w", err)
	}
	return supportBundleRunResult{Path: absolutePath, Size: info.Size(), Collectors: results}, nil
}

func runSupportBundleCollector(ctx context.Context, collector supportBundleCollector) (data []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("collector panic: %v", recovered)
		}
	}()
	return collector.Collect(ctx)
}

func defaultSupportBundleCollectors(_ bool) []supportBundleCollector {
	return []supportBundleCollector{
		{Name: "doctor", Filename: "doctor.json", Collect: collectSupportDoctor},
		{Name: "net-doctor", Filename: "net-doctor.json", Collect: collectSupportNetDoctor},
		{Name: "storage-report", Filename: "storage-report.json", Collect: collectSupportStorageReport},
		{Name: "flux-discovery", Filename: "flux-discovery.json", Collect: collectSupportFluxDiscovery},
		{Name: "flux-summaries", Filename: "flux-summaries.json", Collect: collectSupportFluxSummaries},
		{Name: "etcd-status", Filename: "etcd-status.json", Collect: collectSupportEtcdStatus},
		{Name: "certificates", Filename: "certificates.json", SSH: true, Collect: collectSupportCertificates},
		{Name: "flatcar-os-status", Filename: "flatcar-os-status.json", SSH: true, Collect: flatcar.CollectOSStatusJSON},
		{Name: "upgrade-status", Filename: "upgrade-status.json", Collect: collectSupportUpgradeStatus},
		{Name: "kubectl-version", Filename: "kubectl-version.json", Collect: collectSupportKubectlVersion},
		{Name: "cli-version", Filename: "cli-version.json", Collect: collectSupportCLIVersion},
		{Name: "events", Filename: "events.json", Collect: collectSupportEvents},
		{Name: "nodes-wide", Filename: "nodes-wide.txt", Collect: collectSupportNodesWide},
	}
}

func collectSupportDoctor(ctx context.Context) ([]byte, error) {
	return json.MarshalIndent(buildDoctorReportContext(ctx, "", doctorDefaultPendingGrace), "", "  ")
}

func collectSupportNetDoctor(ctx context.Context) ([]byte, error) {
	return json.MarshalIndent(buildNetDoctorReport(ctx, nil), "", "  ")
}

func collectSupportStorageReport(ctx context.Context) ([]byte, error) {
	return json.MarshalIndent(buildStorageReport(ctx, "", storageDefaultCephWarnPercent), "", "  ")
}

func collectSupportFluxDiscovery(ctx context.Context) ([]byte, error) {
	var kustomizations fluxKustomizationList
	if err := kubectlGetJSONContext(ctx, "", fluxKustomizationResource, &kustomizations); err != nil {
		return nil, err
	}
	return json.MarshalIndent(summarizeKustomizations(kustomizations.Items, ""), "", "  ")
}

func collectSupportFluxSummaries(ctx context.Context) ([]byte, error) {
	var kustomizations fluxKustomizationList
	if err := kubectlGetJSONContext(ctx, "", fluxKustomizationResource, &kustomizations); err != nil {
		return nil, err
	}
	var releases fluxHelmReleaseList
	if err := kubectlGetJSONContext(ctx, "", fluxHelmReleaseResource, &releases); err != nil {
		return nil, err
	}
	helm := make([]fluxTreeHelmNode, 0, len(releases.Items))
	for _, release := range releases.Items {
		ready, message := fluxReady(release.Status.Conditions)
		helm = append(helm, fluxTreeHelmNode{
			Name: release.Metadata.Name, Namespace: release.Metadata.Namespace, Ready: ready,
			Suspended: release.Spec.Suspend, Revision: release.Status.LastAppliedRevision, Message: message,
		})
	}
	sort.Slice(helm, func(i, j int) bool {
		return namespacedName(helm[i].Namespace, helm[i].Name) < namespacedName(helm[j].Namespace, helm[j].Name)
	})
	report := supportBundleFluxSummary{Kustomizations: summarizeKustomizations(kustomizations.Items, ""), HelmReleases: helm}
	return json.MarshalIndent(report, "", "  ")
}

func collectSupportEtcdStatus(ctx context.Context) ([]byte, error) {
	report, err := buildEtcdStatus(ctx, config.Get().State.EtcdBackup.Dir, etcdDefaultStaleAfter)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(report, "", "  ")
}

func collectSupportCertificates(ctx context.Context) ([]byte, error) {
	report, _, err := runCertsWorkflow(ctx, certDefaultWarnDays, false, false)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(report, "", "  ")
}

func collectSupportUpgradeStatus(ctx context.Context) ([]byte, error) {
	report, err := buildUpgradeStatusReport(ctx)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(report, "", "  ")
}

func collectSupportKubectlVersion(ctx context.Context) ([]byte, error) {
	return supportBundleKubectlOutputFn(ctx, "version", "-o", "json")
}

func collectSupportCLIVersion(_ context.Context) ([]byte, error) {
	return json.MarshalIndent(map[string]string{"version": supportBundleCLIVersion(), "go_version": runtime.Version()}, "", "  ")
}

func collectSupportEvents(ctx context.Context) ([]byte, error) {
	raw, err := supportBundleKubectlOutputFn(ctx, "get", "events", "-A", "--sort-by=.lastTimestamp", "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		APIVersion string            `json:"apiVersion,omitempty"`
		Kind       string            `json:"kind,omitempty"`
		Metadata   json.RawMessage   `json:"metadata,omitempty"`
		Items      []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse events JSON: %w", err)
	}
	if len(list.Items) > 200 {
		list.Items = list.Items[len(list.Items)-200:]
	}
	return json.MarshalIndent(list, "", "  ")
}

func collectSupportNodesWide(ctx context.Context) ([]byte, error) {
	return supportBundleKubectlOutputFn(ctx, "get", "nodes", "-o", "wide")
}

func supportBundleCLIVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func supportBundleVersions(root *os.Root) map[string]string {
	versions := map[string]string{"go": runtime.Version()}
	raw, err := root.ReadFile("kubectl-version.json")
	if err != nil {
		return versions
	}
	var version struct {
		ClientVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if json.Unmarshal(raw, &version) == nil {
		if version.ClientVersion.GitVersion != "" {
			versions["kubernetes_client"] = version.ClientVersion.GitVersion
		}
		if version.ServerVersion.GitVersion != "" {
			versions["kubernetes_server"] = version.ServerVersion.GitVersion
		}
	}
	return versions
}

func scanSupportBundle(root *os.Root, contents []string) error {
	for _, name := range contents {
		data, err := root.ReadFile(name)
		if err != nil {
			return fmt.Errorf("security scan read %s: %w", name, err)
		}
		for _, forbidden := range supportBundleForbiddenPatterns {
			if forbidden.pattern.Match(data) {
				return fmt.Errorf("refusing to write support bundle: security scan found possible %s in %s", forbidden.name, name)
			}
		}
	}
	return nil
}

func writeSupportBundleArchive(sourceDir, outputPath string, contents []string) (err error) {
	outputDir := filepath.Dir(outputPath)
	temporary, err := os.CreateTemp(outputDir, ".homeops-support-bundle-*.tmp")
	if err != nil {
		return fmt.Errorf("create support bundle archive: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure support bundle archive: %w", err)
	}
	gzipWriter := gzip.NewWriter(temporary)
	tarWriter := tar.NewWriter(gzipWriter)
	root, err := os.OpenRoot(sourceDir)
	if err != nil {
		return fmt.Errorf("open support bundle source: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range contents {
		info, statErr := root.Stat(name)
		if statErr != nil {
			err = fmt.Errorf("stat support bundle entry %s: %w", name, statErr)
			break
		}
		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			err = fmt.Errorf("create tar header for %s: %w", name, headerErr)
			break
		}
		header.Name, header.Mode = filepath.ToSlash(name), 0o600
		if writeErr := tarWriter.WriteHeader(header); writeErr != nil {
			err = fmt.Errorf("write tar header for %s: %w", name, writeErr)
			break
		}
		file, openErr := root.Open(name)
		if openErr != nil {
			err = fmt.Errorf("open support bundle entry %s: %w", name, openErr)
			break
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			err = fmt.Errorf("archive support bundle entry %s: %w", name, copyErr)
			break
		}
		if closeErr != nil {
			err = fmt.Errorf("close support bundle entry %s: %w", name, closeErr)
			break
		}
	}
	if closeErr := tarWriter.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close support bundle tar: %w", closeErr)
	}
	if closeErr := gzipWriter.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close support bundle gzip: %w", closeErr)
	}
	if syncErr := temporary.Sync(); err == nil && syncErr != nil {
		err = fmt.Errorf("sync support bundle archive: %w", syncErr)
	}
	if closeErr := temporary.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close support bundle archive: %w", closeErr)
	}
	if err != nil {
		return err
	}
	if renameErr := os.Rename(temporaryPath, outputPath); renameErr != nil {
		return fmt.Errorf("publish support bundle archive: %w", renameErr)
	}
	return nil
}

func renderSupportBundleStatuses(results []supportBundleCollectorResult) string {
	rows := make([][]string, 0, len(results))
	for _, result := range results {
		detail := result.File
		if result.Status == "ERROR" {
			detail = result.Error
		}
		rows = append(rows, []string{result.Status, result.Name, fmt.Sprintf("%dms", result.DurationMS), detail})
	}
	return ui.Table([]string{"STATUS", "COLLECTOR", "DURATION", "FILE/DETAIL"}, rows)
}
