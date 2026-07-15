package kubernetes

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"homeops-cli/internal/ui"
)

const supportBundleArchiveEntryLimit = 64 << 20

type supportBundleArchive struct {
	Path     string
	Manifest supportBundleManifest
	Entries  map[string][]byte
}

type supportBundleDriftCounts struct {
	NewFail  int `json:"new_fail"`
	NewWarn  int `json:"new_warn"`
	Resolved int `json:"resolved"`
	Changed  int `json:"changed"`
}

type supportBundleCollectorDrift struct {
	Collector string `json:"collector"`
	State     string `json:"state"`
	supportBundleDriftCounts
	Detail string `json:"detail"`
}

type supportBundleDriftFinding struct {
	Collector      string `json:"collector"`
	Classification string `json:"classification"`
	Key            string `json:"key"`
	OldStatus      string `json:"old_status,omitempty"`
	NewStatus      string `json:"new_status,omitempty"`
	OldValue       string `json:"old_value,omitempty"`
	NewValue       string `json:"new_value,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

type supportBundleDriftReport struct {
	OldBundle  string                        `json:"old_bundle"`
	NewBundle  string                        `json:"new_bundle"`
	Summary    supportBundleDriftCounts      `json:"summary"`
	Collectors []supportBundleCollectorDrift `json:"collectors"`
	Findings   []supportBundleDriftFinding   `json:"findings"`
}

type supportBundleDriftRow struct {
	Key    string
	Status string
	Detail string
}

type supportBundleCollectorFile struct {
	File   string
	Status string
}

func loadSupportBundleArchive(archivePath string) (supportBundleArchive, error) {
	absolutePath, err := filepathAbsClean(archivePath)
	if err != nil {
		return supportBundleArchive{}, err
	}
	file, err := os.Open(absolutePath) // #nosec G304 -- the user explicitly selects the bundle to compare.
	if err != nil {
		return supportBundleArchive{}, fmt.Errorf("open %s: %w", absolutePath, err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return supportBundleArchive{}, fmt.Errorf("read gzip stream: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()

	entries := map[string][]byte{}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return supportBundleArchive{}, fmt.Errorf("read tar stream: %w", nextErr)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}
		if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return supportBundleArchive{}, fmt.Errorf("archive contains unsafe entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > supportBundleArchiveEntryLimit {
			return supportBundleArchive{}, fmt.Errorf("archive entry %q exceeds %d bytes", name, supportBundleArchiveEntryLimit)
		}
		data, readErr := io.ReadAll(io.LimitReader(tarReader, supportBundleArchiveEntryLimit+1))
		if readErr != nil {
			return supportBundleArchive{}, fmt.Errorf("read archive entry %q: %w", name, readErr)
		}
		if len(data) > supportBundleArchiveEntryLimit {
			return supportBundleArchive{}, fmt.Errorf("archive entry %q exceeds %d bytes", name, supportBundleArchiveEntryLimit)
		}
		entries[name] = data
	}
	manifestRaw, ok := entries["manifest.json"]
	if !ok {
		return supportBundleArchive{}, fmt.Errorf("archive does not contain manifest.json")
	}
	var manifest supportBundleManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return supportBundleArchive{}, fmt.Errorf("parse manifest.json: %w", err)
	}
	return supportBundleArchive{Path: absolutePath, Manifest: manifest, Entries: entries}, nil
}

func filepathAbsClean(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("bundle path is empty")
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve bundle path: %w", err)
	}
	return absolute, nil
}

func compareSupportBundles(oldBundle, newBundle supportBundleArchive) supportBundleDriftReport {
	report := supportBundleDriftReport{OldBundle: oldBundle.Path, NewBundle: newBundle.Path}
	oldCollectors := supportBundleCollectorFiles(oldBundle)
	newCollectors := supportBundleCollectorFiles(newBundle)
	names := unionSortedKeys(oldCollectors, newCollectors)
	for _, name := range names {
		oldCollector, oldOK := oldCollectors[name]
		newCollector, newOK := newCollectors[name]
		collector := supportBundleCollectorDrift{Collector: name}
		switch {
		case !oldOK:
			collector.State, collector.Detail = "ADDED", "collector added in new bundle"
		case !newOK:
			collector.State, collector.Detail = "REMOVED", "collector absent from new bundle"
		case name == "events" || name == "nodes-wide":
			collector.State = "PRESENCE"
			collector.Detail = presenceDetail(oldCollector.File, newCollector.File)
		default:
			findings, compareErr := compareSupportBundleCollector(name, oldCollector, newCollector, oldBundle.Entries, newBundle.Entries)
			if compareErr != nil {
				collector.State, collector.Detail = "INCOMPARABLE", "incomparable: "+compareErr.Error()
			} else {
				collector.State = "COMPARED"
				collector.Detail = "no health drift"
				if len(findings) > 0 {
					collector.Detail = fmt.Sprintf("%d changed finding(s)", len(findings))
				}
				for _, finding := range findings {
					addDriftClassification(&collector.supportBundleDriftCounts, finding.Classification)
					addDriftClassification(&report.Summary, finding.Classification)
				}
				report.Findings = append(report.Findings, findings...)
			}
		}
		report.Collectors = append(report.Collectors, collector)
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		left, right := report.Findings[i], report.Findings[j]
		if driftClassificationRank(left.Classification) != driftClassificationRank(right.Classification) {
			return driftClassificationRank(left.Classification) < driftClassificationRank(right.Classification)
		}
		if left.Collector != right.Collector {
			return left.Collector < right.Collector
		}
		return left.Key < right.Key
	})
	return report
}

func supportBundleCollectorFiles(bundle supportBundleArchive) map[string]supportBundleCollectorFile {
	files := map[string]supportBundleCollectorFile{}
	referenced := map[string]struct{}{}
	for _, collector := range bundle.Manifest.Collectors {
		if strings.TrimSpace(collector.Name) == "" {
			continue
		}
		files[collector.Name] = supportBundleCollectorFile{File: collector.File, Status: collector.Status}
		if collector.File != "" {
			referenced[collector.File] = struct{}{}
		}
	}
	known := map[string]string{
		"doctor.json": "doctor", "net-doctor.json": "net-doctor", "storage-report.json": "storage-report",
		"flux-discovery.json": "flux-discovery", "flux-summaries.json": "flux-summaries", "etcd-status.json": "etcd-status",
		"certificates.json": "certificates", "flatcar-os-status.json": "flatcar-os-status", "upgrade-status.json": "upgrade-status",
		"kubectl-version.json": "kubectl-version", "cli-version.json": "cli-version", "events.json": "events", "nodes-wide.txt": "nodes-wide",
	}
	for filename, name := range known {
		if _, exists := bundle.Entries[filename]; !exists {
			continue
		}
		if current, exists := files[name]; !exists || current.File == "" {
			files[name] = supportBundleCollectorFile{File: filename, Status: "OK"}
		}
		referenced[filename] = struct{}{}
	}
	for filename := range bundle.Entries {
		if filename == "manifest.json" {
			continue
		}
		if _, exists := referenced[filename]; exists {
			continue
		}
		name := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
		if _, exists := files[name]; !exists {
			files[name] = supportBundleCollectorFile{File: filename, Status: "OK"}
		}
	}
	return files
}

func unionSortedKeys(left, right map[string]supportBundleCollectorFile) []string {
	values := map[string]struct{}{}
	for key := range left {
		values[key] = struct{}{}
	}
	for key := range right {
		values[key] = struct{}{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func presenceDetail(oldFile, newFile string) string {
	switch {
	case oldFile != "" && newFile != "":
		return "present in both; content diff skipped"
	case oldFile == "" && newFile == "":
		return "absent in both; content diff skipped"
	case oldFile == "":
		return "present only in new bundle; content diff skipped"
	default:
		return "present only in old bundle; content diff skipped"
	}
}

func compareSupportBundleCollector(name string, oldCollector, newCollector supportBundleCollectorFile, oldEntries, newEntries map[string][]byte) ([]supportBundleDriftFinding, error) {
	oldRaw, oldOK := oldEntries[oldCollector.File]
	newRaw, newOK := newEntries[newCollector.File]
	if oldCollector.File == "" || !oldOK {
		return nil, fmt.Errorf("old collector data unavailable (%s)", collectorAvailability(oldCollector))
	}
	if newCollector.File == "" || !newOK {
		return nil, fmt.Errorf("new collector data unavailable (%s)", collectorAvailability(newCollector))
	}
	if !strings.HasSuffix(oldCollector.File, ".json") || !strings.HasSuffix(newCollector.File, ".json") {
		return nil, fmt.Errorf("collector output is not structured JSON")
	}
	if name == "kubectl-version" || name == "cli-version" {
		oldValues, err := extractVersionValues(name, oldRaw)
		if err != nil {
			return nil, fmt.Errorf("old report: %w", err)
		}
		newValues, err := extractVersionValues(name, newRaw)
		if err != nil {
			return nil, fmt.Errorf("new report: %w", err)
		}
		return diffVersionValues(name, oldValues, newValues), nil
	}
	oldRows, oldVersions, err := extractDriftRows(name, oldRaw)
	if err != nil {
		return nil, fmt.Errorf("old report: %w", err)
	}
	newRows, newVersions, err := extractDriftRows(name, newRaw)
	if err != nil {
		return nil, fmt.Errorf("new report: %w", err)
	}
	findings := diffDriftRows(name, oldRows, newRows)
	findings = append(findings, diffVersionValues(name, oldVersions, newVersions)...)
	return findings, nil
}

func collectorAvailability(collector supportBundleCollectorFile) string {
	if collector.Status != "" {
		return strings.ToLower(collector.Status)
	}
	return "missing file"
}

func diffDriftRows(collector string, oldRows, newRows []supportBundleDriftRow) []supportBundleDriftFinding {
	oldByKey := driftRowsByKey(oldRows)
	newByKey := driftRowsByKey(newRows)
	keys := map[string]struct{}{}
	for key := range oldByKey {
		keys[key] = struct{}{}
	}
	for key := range newByKey {
		keys[key] = struct{}{}
	}
	var findings []supportBundleDriftFinding
	for key := range keys {
		oldRow, oldOK := oldByKey[key]
		newRow, newOK := newByKey[key]
		classification := classifyDriftStatus(oldRow.Status, oldOK, newRow.Status, newOK)
		if classification == "" {
			continue
		}
		finding := supportBundleDriftFinding{Collector: collector, Classification: classification, Key: key}
		if oldOK {
			finding.OldStatus = oldRow.Status
		}
		if newOK {
			finding.NewStatus = newRow.Status
		}
		finding.Detail = newRow.Detail
		if !newOK || strings.TrimSpace(finding.Detail) == "" {
			finding.Detail = oldRow.Detail
		}
		findings = append(findings, finding)
	}
	return findings
}

func driftRowsByKey(rows []supportBundleDriftRow) map[string]supportBundleDriftRow {
	result := make(map[string]supportBundleDriftRow, len(rows))
	for _, row := range rows {
		result[row.Key] = row
	}
	return result
}

func classifyDriftStatus(oldStatus string, oldOK bool, newStatus string, newOK bool) string {
	oldSeverity, newSeverity := driftStatusSeverity(oldStatus), driftStatusSeverity(newStatus)
	switch {
	case newOK && newSeverity == 3 && (!oldOK || oldSeverity < newSeverity):
		return "NEW-FAIL"
	case newOK && newSeverity == 2 && (!oldOK || oldSeverity < newSeverity):
		return "NEW-WARN"
	case oldOK && oldSeverity >= 2 && (!newOK || newSeverity <= 1):
		return "RESOLVED"
	case oldOK && newOK && !strings.EqualFold(strings.TrimSpace(oldStatus), strings.TrimSpace(newStatus)):
		return "CHANGED"
	default:
		return ""
	}
}

func driftStatusSeverity(status string) int {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "FAIL", "FAILED", "ERROR", "UNHEALTHY", "FALSE":
		return 3
	case "WARN", "WARNING", "PENDING", "UNKNOWN", "ACTIVE", "REBOOT-REQUIRED", "REBOOT REQUIRED":
		return 2
	default:
		return 1
	}
}

func diffVersionValues(collector string, oldValues, newValues map[string]string) []supportBundleDriftFinding {
	keys := map[string]struct{}{}
	for key := range oldValues {
		keys[key] = struct{}{}
	}
	for key := range newValues {
		keys[key] = struct{}{}
	}
	var findings []supportBundleDriftFinding
	for key := range keys {
		oldValue, oldOK := oldValues[key]
		newValue, newOK := newValues[key]
		if !oldOK || !newOK || oldValue == newValue {
			continue
		}
		findings = append(findings, supportBundleDriftFinding{
			Collector: collector, Classification: "CHANGED", Key: key,
			OldValue: oldValue, NewValue: newValue, Detail: oldValue + " -> " + newValue,
		})
	}
	return findings
}

func addDriftClassification(counts *supportBundleDriftCounts, classification string) {
	switch classification {
	case "NEW-FAIL":
		counts.NewFail++
	case "NEW-WARN":
		counts.NewWarn++
	case "RESOLVED":
		counts.Resolved++
	case "CHANGED":
		counts.Changed++
	}
}

func driftClassificationRank(classification string) int {
	switch classification {
	case "NEW-FAIL":
		return 0
	case "NEW-WARN":
		return 1
	case "RESOLVED":
		return 2
	default:
		return 3
	}
}

func renderSupportBundleDrift(report supportBundleDriftReport) string {
	rows := make([][]string, 0, len(report.Collectors))
	for _, collector := range report.Collectors {
		rows = append(rows, []string{
			collector.Collector, strconv.Itoa(collector.NewFail), strconv.Itoa(collector.NewWarn),
			strconv.Itoa(collector.Resolved), strconv.Itoa(collector.Changed), collector.Detail,
		})
	}
	var rendered strings.Builder
	rendered.WriteString("HEALTH DRIFT\n")
	rendered.WriteString(ui.Table([]string{"COLLECTOR", "NEW-FAIL", "NEW-WARN", "RESOLVED", "CHANGED", "DETAIL"}, rows))
	fmt.Fprintf(&rendered, "\nDrift totals: NEW-FAIL=%d NEW-WARN=%d RESOLVED=%d CHANGED=%d",
		report.Summary.NewFail, report.Summary.NewWarn, report.Summary.Resolved, report.Summary.Changed)
	if len(report.Findings) > 0 {
		rendered.WriteString("\nChanged findings:")
		for _, finding := range report.Findings {
			fmt.Fprintf(&rendered, "\n- %s %s %s", finding.Classification, finding.Collector, finding.Key)
			switch {
			case finding.OldValue != "" || finding.NewValue != "":
				fmt.Fprintf(&rendered, ": %s -> %s", displayDriftValue(finding.OldValue), displayDriftValue(finding.NewValue))
			case finding.OldStatus != "" || finding.NewStatus != "":
				fmt.Fprintf(&rendered, ": %s -> %s", displayDriftValue(finding.OldStatus), displayDriftValue(finding.NewStatus))
			}
			if strings.TrimSpace(finding.Detail) != "" && finding.Detail != finding.OldValue+" -> "+finding.NewValue {
				fmt.Fprintf(&rendered, " (%s)", finding.Detail)
			}
		}
	}
	return rendered.String()
}

func displayDriftValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "absent"
	}
	return value
}

func extractDriftRows(collector string, raw []byte) ([]supportBundleDriftRow, map[string]string, error) {
	// flux-discovery is a JSON array; decode it before the object-shaped path.
	if collector == "flux-discovery" {
		return extractFluxArrayRows(raw)
	}
	root, err := decodeDriftObject(raw)
	if err != nil {
		return nil, nil, err
	}
	switch collector {
	case "doctor", "net-doctor":
		rows, err := extractCheckRows(root, "checks", []string{"group", "kind", "name"})
		return rows, nil, err
	case "certificates":
		active := root
		if after, ok := driftObject(root["after"]); ok {
			active = after
		} else if before, ok := driftObject(root["before"]); ok {
			active = before
		}
		rows, err := extractCheckRows(active, "checks", []string{"node", "name"})
		return rows, nil, err
	case "storage-report":
		rows, err := extractStorageDriftRows(root)
		return rows, nil, err
	case "etcd-status":
		rows, err := extractEtcdDriftRows(root)
		return rows, nil, err
	case "upgrade-status":
		rows, versions, err := extractUpgradeDriftRows(root)
		return rows, versions, err
	case "flatcar-os-status":
		rows, versions, err := extractFlatcarDriftRows(root)
		return rows, versions, err
	case "flux-discovery":
		return extractFluxArrayRows(raw)
	case "flux-summaries":
		rows, err := extractFluxSummaryRows(root)
		return rows, nil, err
	default:
		return nil, nil, fmt.Errorf("unsupported collector format")
	}
}

func decodeDriftObject(raw []byte) (map[string]any, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	root, ok := driftObject(value)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return root, nil
}

func driftObject(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}

func driftArray(root map[string]any, field string) ([]any, bool) {
	value, exists := root[field]
	if !exists {
		return nil, false
	}
	if value == nil {
		return nil, true
	}
	result, ok := value.([]any)
	return result, ok
}

func driftString(root map[string]any, field string) string {
	value, exists := root[field]
	if !exists || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func extractCheckRows(root map[string]any, field string, identityFields []string) ([]supportBundleDriftRow, error) {
	values, ok := driftArray(root, field)
	if !ok {
		return nil, fmt.Errorf("missing or invalid %s array", field)
	}
	rows := make([]supportBundleDriftRow, 0, len(values))
	for index, value := range values {
		item, ok := driftObject(value)
		if !ok {
			return nil, fmt.Errorf("%s[%d] is not an object", field, index)
		}
		key := driftIdentity(item, identityFields...)
		status := driftString(item, "status")
		if key == "" || status == "" {
			return nil, fmt.Errorf("%s[%d] lacks identity or status fields", field, index)
		}
		rows = append(rows, supportBundleDriftRow{Key: key, Status: status, Detail: driftString(item, "detail")})
	}
	return uniqueDriftRows(rows)
}

func driftIdentity(item map[string]any, fields ...string) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(driftString(item, field)); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, "/")
}

func uniqueDriftRows(rows []supportBundleDriftRow) ([]supportBundleDriftRow, error) {
	seen := map[string]struct{}{}
	for _, row := range rows {
		if _, exists := seen[row.Key]; exists {
			return nil, fmt.Errorf("duplicate finding identity %q", row.Key)
		}
		seen[row.Key] = struct{}{}
	}
	return rows, nil
}

func extractStorageDriftRows(root map[string]any) ([]supportBundleDriftRow, error) {
	shapes := []struct {
		Field         string
		Prefix        string
		Identity      []string
		DefaultStatus string
	}{
		{Field: "orphaned_pvcs", Prefix: "orphaned-pvc", Identity: []string{"namespace", "name"}, DefaultStatus: "WARN"},
		{Field: "pv_issues", Prefix: "pv", Identity: []string{"name"}},
		{Field: "ceph_capacity", Prefix: "ceph", Identity: []string{"namespace", "name"}},
		{Field: "provisioned_vs_capacity", Prefix: "provisioning", Identity: []string{"storage_class"}},
		{Field: "volsync_coverage_gaps", Prefix: "volsync", Identity: []string{"namespace", "pvc"}, DefaultStatus: "WARN"},
	}
	var rows []supportBundleDriftRow
	recognized := false
	for _, shape := range shapes {
		values, ok := driftArray(root, shape.Field)
		if !ok {
			continue
		}
		recognized = true
		for index, value := range values {
			item, ok := driftObject(value)
			if !ok {
				return nil, fmt.Errorf("%s[%d] is not an object", shape.Field, index)
			}
			identity := driftIdentity(item, shape.Identity...)
			if identity == "" {
				return nil, fmt.Errorf("%s[%d] lacks identity fields", shape.Field, index)
			}
			status := driftString(item, "status")
			if status == "" {
				status = shape.DefaultStatus
			}
			if status == "" {
				return nil, fmt.Errorf("%s[%d] lacks status", shape.Field, index)
			}
			rows = append(rows, supportBundleDriftRow{Key: shape.Prefix + "/" + identity, Status: status, Detail: storageDriftDetail(shape.Field, item)})
		}
	}
	if capacity, ok := driftArray(root, "ceph_capacity"); ok && len(capacity) == 0 {
		rows = append(rows, supportBundleDriftRow{Key: "ceph/no-capacity", Status: "WARN", Detail: "no CephCluster capacity reported"})
	}
	if errorsValue, ok := root["errors"]; ok {
		recognized = true
		if errorsValue != nil {
			errorsList, valid := errorsValue.([]any)
			if !valid {
				return nil, fmt.Errorf("invalid errors array")
			}
			for index, value := range errorsList {
				detail, valid := value.(string)
				if !valid || strings.TrimSpace(detail) == "" {
					return nil, fmt.Errorf("errors[%d] is not a string", index)
				}
				rows = append(rows, supportBundleDriftRow{Key: "api-error/" + detail, Status: "FAIL", Detail: detail})
			}
		}
	}
	if !recognized {
		return nil, fmt.Errorf("no recognized storage finding arrays")
	}
	return uniqueDriftRows(rows)
}

func storageDriftDetail(field string, item map[string]any) string {
	switch field {
	case "orphaned_pvcs", "volsync_coverage_gaps":
		return strings.TrimSpace(strings.Join([]string{driftString(item, "storage_class"), driftString(item, "size")}, " "))
	case "pv_issues":
		return strings.TrimSpace(strings.Join([]string{driftString(item, "phase"), driftString(item, "claim")}, " "))
	case "ceph_capacity":
		return strings.TrimSpace(strings.Join([]string{driftString(item, "health"), driftString(item, "used_percent") + "% used"}, " "))
	default:
		return "ratio " + driftString(item, "ratio")
	}
}

func extractEtcdDriftRows(root map[string]any) ([]supportBundleDriftRow, error) {
	endpoints, ok := driftArray(root, "endpoints")
	if !ok {
		return nil, fmt.Errorf("missing or invalid endpoints array")
	}
	rows := make([]supportBundleDriftRow, 0, len(endpoints)+2)
	for index, value := range endpoints {
		item, ok := driftObject(value)
		if !ok {
			return nil, fmt.Errorf("endpoints[%d] is not an object", index)
		}
		key := driftString(item, "endpoint")
		healthy, valid := item["healthy"].(bool)
		if key == "" || !valid {
			return nil, fmt.Errorf("endpoints[%d] lacks endpoint or healthy fields", index)
		}
		status := "FAIL"
		if healthy {
			status = "OK"
		}
		rows = append(rows, supportBundleDriftRow{Key: "endpoint/" + key, Status: status, Detail: driftString(item, "error")})
	}
	for _, backup := range []struct{ Field, Key string }{{"backup", "local-backup"}, {"remote_backup", "remote-backup"}} {
		value, exists := root[backup.Field]
		if !exists || value == nil {
			continue
		}
		item, valid := driftObject(value)
		if !valid || driftString(item, "status") == "" {
			return nil, fmt.Errorf("invalid %s object", backup.Field)
		}
		rows = append(rows, supportBundleDriftRow{Key: backup.Key, Status: driftString(item, "status"), Detail: driftString(item, "detail")})
	}
	return uniqueDriftRows(rows)
}

func extractUpgradeDriftRows(root map[string]any) ([]supportBundleDriftRow, map[string]string, error) {
	var rows []supportBundleDriftRow
	versions := map[string]string{}
	nodes, ok := driftArray(root, "nodes")
	if !ok {
		return nil, nil, fmt.Errorf("missing or invalid nodes array")
	}
	for index, value := range nodes {
		item, valid := driftObject(value)
		if !valid {
			return nil, nil, fmt.Errorf("nodes[%d] is not an object", index)
		}
		name, status := driftString(item, "name"), driftString(item, "status")
		if name == "" || status == "" {
			return nil, nil, fmt.Errorf("nodes[%d] lacks name or status", index)
		}
		rows = append(rows, supportBundleDriftRow{Key: "node/" + name, Status: status, Detail: "target " + driftString(item, "target")})
		for _, field := range []string{"kubelet_version", "container_runtime", "os"} {
			if version := driftString(item, field); version != "" {
				versions["node/"+name+"/"+field] = version
			}
		}
	}
	jobs, jobsOK := driftArray(root, "jobs")
	if !jobsOK {
		return nil, nil, fmt.Errorf("missing or invalid jobs array")
	}
	for index, value := range jobs {
		item, valid := driftObject(value)
		if !valid {
			return nil, nil, fmt.Errorf("jobs[%d] is not an object", index)
		}
		key, status := driftIdentity(item, "namespace", "name"), driftString(item, "status")
		if key == "" || status == "" {
			return nil, nil, fmt.Errorf("jobs[%d] lacks identity or status", index)
		}
		rows = append(rows, supportBundleDriftRow{Key: "job/" + key, Status: status, Detail: driftString(item, "reason")})
	}
	if skew, skewOK := driftArray(root, "skew"); skewOK {
		for index, value := range skew {
			item, valid := driftObject(value)
			if !valid || driftString(item, "node") == "" {
				return nil, nil, fmt.Errorf("skew[%d] lacks node identity", index)
			}
			rows = append(rows, supportBundleDriftRow{Key: "skew/" + driftString(item, "node"), Status: "WARN", Detail: driftString(item, "detail")})
		}
	}
	if version := driftString(root, "apiserver_version"); version != "" {
		versions["apiserver_version"] = version
	}
	if plans, plansOK := driftArray(root, "plans"); plansOK {
		for _, value := range plans {
			if item, valid := driftObject(value); valid {
				key := driftIdentity(item, "namespace", "name")
				if key != "" && driftString(item, "target") != "" {
					versions["plan/"+key+"/target"] = driftString(item, "target")
				}
			}
		}
	}
	rows, err := uniqueDriftRows(rows)
	return rows, versions, err
}

func extractFlatcarDriftRows(root map[string]any) ([]supportBundleDriftRow, map[string]string, error) {
	nodes, ok := driftArray(root, "nodes")
	if !ok {
		return nil, nil, fmt.Errorf("missing or invalid nodes array")
	}
	rows := make([]supportBundleDriftRow, 0, len(nodes))
	versions := map[string]string{}
	for index, value := range nodes {
		item, valid := driftObject(value)
		if !valid {
			return nil, nil, fmt.Errorf("nodes[%d] is not an object", index)
		}
		name := driftString(item, "node")
		if name == "" {
			return nil, nil, fmt.Errorf("nodes[%d] lacks node identity", index)
		}
		status, detail := "OK", driftString(item, "update_status")
		if errorDetail := driftString(item, "error"); errorDetail != "" {
			status, detail = "FAIL", errorDetail
		} else if reboot, _ := item["reboot_needed"].(bool); reboot {
			status, detail = "WARN", "reboot needed"
		}
		rows = append(rows, supportBundleDriftRow{Key: "node/" + name, Status: status, Detail: detail})
		for _, field := range []string{"flatcar_version", "kernel"} {
			if version := driftString(item, field); version != "" {
				versions["node/"+name+"/"+field] = version
			}
		}
	}
	rows, err := uniqueDriftRows(rows)
	return rows, versions, err
}

func extractFluxArrayRows(raw []byte) ([]supportBundleDriftRow, map[string]string, error) {
	var values []any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return nil, nil, fmt.Errorf("parse JSON array: %w", err)
	}
	rows, err := fluxRows(values, "kustomization")
	return rows, nil, err
}

func extractFluxSummaryRows(root map[string]any) ([]supportBundleDriftRow, error) {
	var rows []supportBundleDriftRow
	for _, shape := range []struct{ Field, Prefix string }{{"kustomizations", "kustomization"}, {"helm_releases", "helmrelease"}} {
		values, ok := driftArray(root, shape.Field)
		if !ok {
			return nil, fmt.Errorf("missing or invalid %s array", shape.Field)
		}
		extracted, err := fluxRows(values, shape.Prefix)
		if err != nil {
			return nil, err
		}
		rows = append(rows, extracted...)
	}
	return uniqueDriftRows(rows)
}

func fluxRows(values []any, prefix string) ([]supportBundleDriftRow, error) {
	rows := make([]supportBundleDriftRow, 0, len(values))
	for index, value := range values {
		item, ok := driftObject(value)
		if !ok {
			return nil, fmt.Errorf("%s[%d] is not an object", prefix, index)
		}
		key, ready := driftIdentity(item, "namespace", "name"), driftString(item, "ready")
		if key == "" || ready == "" {
			return nil, fmt.Errorf("%s[%d] lacks identity or ready fields", prefix, index)
		}
		status := ready
		if suspended, _ := item["suspended"].(bool); suspended {
			status = "WARN"
		}
		rows = append(rows, supportBundleDriftRow{Key: prefix + "/" + key, Status: status, Detail: driftString(item, "message")})
	}
	return uniqueDriftRows(rows)
}

func extractVersionValues(collector string, raw []byte) (map[string]string, error) {
	root, err := decodeDriftObject(raw)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	switch collector {
	case "cli-version":
		for _, field := range []string{"version", "go_version"} {
			if value := driftString(root, field); value != "" {
				values[field] = value
			}
		}
	case "kubectl-version":
		for _, shape := range []struct{ Field, Key string }{{"clientVersion", "client"}, {"serverVersion", "server"}} {
			if versionRoot, ok := driftObject(root[shape.Field]); ok {
				if value := driftString(versionRoot, "gitVersion"); value != "" {
					values[shape.Key] = value
				}
			}
		}
		if value := driftString(root, "kustomizeVersion"); value != "" {
			values["kustomize"] = value
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("no recognized version fields")
	}
	return values, nil
}
