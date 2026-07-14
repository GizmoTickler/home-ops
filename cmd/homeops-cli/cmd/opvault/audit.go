package opvault

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
	"homeops-cli/internal/ui"
)

var opAuditKubectlOutputFn = func(args ...string) ([]byte, error) {
	return common.Output("kubectl", args...)
}

const opAuditDefaultTimeout = 5 * time.Minute

const opItemListWorkerLimit = 4

type auditOpItem struct {
	ID    string
	Title string
	Vault string
}

var opAuditKubectlOutputCtxFn = func(ctx context.Context, args ...string) ([]byte, error) {
	result, err := common.RunCommand(ctx, common.CommandOptions{
		Name:    "kubectl",
		Args:    args,
		Timeout: opAuditDefaultTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
	}
	return []byte(result.Stdout), nil
}

var runOpCtxFn = func(ctx context.Context, args ...string) ([]byte, error) {
	result, err := common.RunCommand(ctx, common.CommandOptions{
		Name:    "op",
		Args:    args,
		Timeout: opAuditDefaultTimeout,
	})
	if err != nil {
		return nil, opAuditCommandError(args, result.Stderr, err)
	}
	return []byte(result.Stdout), nil
}

func opAuditCommandError(args []string, stderr string, err error) error {
	contextLabel := "op"
	if len(args) >= 2 {
		contextLabel = "op " + strings.Join(args[:2], " ")
	} else if len(args) == 1 {
		contextLabel = "op " + args[0]
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		line = strings.TrimSpace(opErrorPrefix.ReplaceAllString(strings.TrimSpace(line), ""))
		if line != "" {
			return fmt.Errorf("%s: %s", contextLabel, line)
		}
	}
	return fmt.Errorf("%s: %w", contextLabel, err)
}

type auditStatus string

const (
	auditPass auditStatus = "PASS"
	auditWarn auditStatus = "WARN"
	auditFail auditStatus = "FAIL"
)

type auditSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

type auditExternalSecret struct {
	ExternalSecret string      `json:"external_secret"`
	Namespace      string      `json:"namespace"`
	Status         auditStatus `json:"status"`
	Reason         string      `json:"reason,omitempty"`
	Message        string      `json:"message,omitempty"`
}

type auditReference struct {
	Item           string `json:"item"`
	ExternalSecret string `json:"external_secret"`
	VaultHint      string `json:"vault_hint,omitempty"`
}

type auditItemFinding struct {
	Item       string      `json:"item"`
	Vault      string      `json:"vault,omitempty"`
	Status     auditStatus `json:"status"`
	References []string    `json:"references,omitempty"`
	Detail     string      `json:"detail"`
}

type auditReport struct {
	Summary         auditSummary          `json:"summary"`
	ExternalSecrets []auditExternalSecret `json:"external_secrets"`
	References      []auditReference      `json:"references"`
	MissingItems    []auditItemFinding    `json:"missing_items"`
	OrphanItems     []auditItemFinding    `json:"orphan_items"`
}

func (r *auditReport) finalize() {
	r.Summary = auditSummary{}
	for _, es := range r.ExternalSecrets {
		r.addSummary(es.Status)
	}
	for _, item := range r.MissingItems {
		r.addSummary(item.Status)
	}
	for _, item := range r.OrphanItems {
		r.addSummary(item.Status)
	}
}

func (r *auditReport) addSummary(status auditStatus) {
	switch status {
	case auditFail:
		r.Summary.Fail++
	case auditWarn:
		r.Summary.Warn++
	default:
		r.Summary.Pass++
	}
}

func (r auditReport) hasFail() bool {
	return r.Summary.Fail > 0
}

func newAuditCommand() *cobra.Command {
	var vault, output string
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit ExternalSecrets against 1Password item inventory",
		Long: strings.TrimSpace(`
Audit ExternalSecrets against the 1Password item inventory.

The audit reports ExternalSecret readiness, items referenced by ExternalSecret
resources but missing from 1Password, and 1Password items not referenced by any
ExternalSecret.

When --vault=all, item listings for accessible vaults run concurrently with a
small worker limit.

Caveat: ExternalSecret references are matched to 1Password items by item title,
because remoteRef keys contain titles rather than item IDs. The 1Password
inventory itself is keyed by item ID so duplicate titles across vaults remain
visible in orphan reporting, but a title referenced by an ExternalSecret is
treated as covering all same-titled inventory items.`),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), opAuditDefaultTimeout)
			defer cancel()
			return runAuditContext(ctx, vault, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "all", "1Password vault to inspect, or all accessible vaults")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	return cmd
}

func runAudit(vault, output string, out io.Writer) error {
	return runAuditContext(context.Background(), vault, output, out)
}

func runAuditContext(ctx context.Context, vault, output string, out io.Writer) error {
	report := buildAuditReportContext(ctx, vault)
	rendered, err := renderAuditReport(report, output)
	if err != nil {
		return err
	}
	if rendered != "" {
		_, _ = fmt.Fprintln(out, rendered)
	}
	if report.hasFail() {
		return fmt.Errorf("op audit found %d failing check(s)", report.Summary.Fail)
	}
	return nil
}

func buildAuditReport(vault string) auditReport {
	return buildAuditReportContext(context.Background(), vault)
}

func buildAuditReportContext(ctx context.Context, vault string) auditReport {
	var report auditReport
	externalSecrets, references, refByItem, err := collectExternalSecretReferencesContext(ctx)
	if err != nil {
		report.ExternalSecrets = append(report.ExternalSecrets, auditExternalSecret{
			ExternalSecret: "externalsecrets",
			Status:         auditFail,
			Message:        err.Error(),
		})
		report.finalize()
		return report
	}
	report.ExternalSecrets = externalSecrets
	report.References = references

	items, err := collectOpItemsContext(ctx, vault)
	if err != nil {
		report.MissingItems = append(report.MissingItems, auditItemFinding{
			Item:   "1password-inventory",
			Status: auditFail,
			Detail: err.Error(),
		})
		report.finalize()
		return report
	}
	itemTitles := map[string]struct{}{}
	for _, item := range items {
		itemTitles[item.Title] = struct{}{}
	}

	for item, refs := range refByItem {
		if _, ok := itemTitles[item]; !ok {
			report.MissingItems = append(report.MissingItems, auditItemFinding{
				Item:       item,
				Status:     auditFail,
				References: refs,
				Detail:     "referenced by ExternalSecret but missing from 1Password inventory",
			})
		}
	}
	for _, item := range items {
		if _, ok := refByItem[item.Title]; !ok {
			report.OrphanItems = append(report.OrphanItems, auditItemFinding{
				Item:   item.Title,
				Vault:  item.Vault,
				Status: auditWarn,
				Detail: "1Password item is not referenced by any ExternalSecret",
			})
		}
	}
	sort.Slice(report.MissingItems, func(i, j int) bool { return report.MissingItems[i].Item < report.MissingItems[j].Item })
	sort.Slice(report.OrphanItems, func(i, j int) bool {
		if report.OrphanItems[i].Vault == report.OrphanItems[j].Vault {
			return report.OrphanItems[i].Item < report.OrphanItems[j].Item
		}
		return report.OrphanItems[i].Vault < report.OrphanItems[j].Vault
	})
	report.finalize()
	return report
}

type externalSecretList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Spec struct {
			SecretStoreRef struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"secretStoreRef"`
			DataFrom []struct {
				Extract *struct {
					Key string `json:"key"`
				} `json:"extract,omitempty"`
			} `json:"dataFrom"`
			Data []struct {
				SecretKey string `json:"secretKey"`
				RemoteRef struct {
					Key      string `json:"key"`
					Property string `json:"property"`
				} `json:"remoteRef"`
			} `json:"data"`
		} `json:"spec"`
		Status struct {
			Conditions []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func collectExternalSecretReferencesContext(ctx context.Context) ([]auditExternalSecret, []auditReference, map[string][]string, error) {
	out, err := opAuditKubectlOutputCtxFn(ctx, "get", "externalsecrets.external-secrets.io", "-A", "-o", "json")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("kubectl get externalsecrets: %w", err)
	}
	var list externalSecretList
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, nil, nil, fmt.Errorf("parse ExternalSecret JSON: %w", err)
	}
	externalSecrets := make([]auditExternalSecret, 0, len(list.Items))
	references := []auditReference{}
	refByItem := map[string][]string{}
	for _, item := range list.Items {
		esName := item.Metadata.Namespace + "/" + item.Metadata.Name
		status := auditPass
		reason, message := "", ""
		readyFound := false
		for _, c := range item.Status.Conditions {
			if c.Type != "Ready" {
				continue
			}
			readyFound = true
			if c.Status != "True" {
				status = auditFail
				reason = c.Reason
				message = c.Message
			}
			break
		}
		if !readyFound {
			status = auditFail
			reason = "ReadyConditionMissing"
		}
		externalSecrets = append(externalSecrets, auditExternalSecret{
			ExternalSecret: esName,
			Namespace:      item.Metadata.Namespace,
			Status:         status,
			Reason:         reason,
			Message:        message,
		})
		addRef := func(key string) {
			key = strings.TrimSpace(key)
			if key == "" {
				return
			}
			references = append(references, auditReference{
				Item:           key,
				ExternalSecret: esName,
				VaultHint:      item.Spec.SecretStoreRef.Name,
			})
			refByItem[key] = append(refByItem[key], esName)
		}
		for _, df := range item.Spec.DataFrom {
			if df.Extract != nil {
				addRef(df.Extract.Key)
			}
		}
		for _, data := range item.Spec.Data {
			addRef(data.RemoteRef.Key)
		}
	}
	sort.Slice(externalSecrets, func(i, j int) bool { return externalSecrets[i].ExternalSecret < externalSecrets[j].ExternalSecret })
	sort.Slice(references, func(i, j int) bool {
		if references[i].Item == references[j].Item {
			return references[i].ExternalSecret < references[j].ExternalSecret
		}
		return references[i].Item < references[j].Item
	})
	return externalSecrets, references, refByItem, nil
}

func collectOpItemsContext(ctx context.Context, vault string) (map[string]auditOpItem, error) {
	vaults := []string{vault}
	if strings.TrimSpace(vault) == "" || vault == "all" {
		list, err := listOpVaultNamesContext(ctx)
		if err != nil {
			return nil, err
		}
		vaults = list
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		items    = map[string]auditOpItem{}
		failures = map[string]error{}
		sem      = make(chan struct{}, min(opItemListWorkerLimit, max(1, len(vaults))))
	)
	for _, vaultName := range vaults {
		vaultName := vaultName
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			listed, err := listOpItemsInVaultContext(ctx, vaultName)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures[vaultName] = err
				return
			}
			for i, item := range listed {
				if item.Title == "" {
					continue
				}
				key := item.ID
				if key == "" {
					key = fmt.Sprintf("%s:%d:%s", vaultName, i, item.Title)
				}
				items[key] = auditOpItem{ID: item.ID, Title: item.Title, Vault: vaultName}
			}
		}()
	}
	wg.Wait()
	for _, vaultName := range vaults {
		if err := failures[vaultName]; err != nil {
			return nil, err
		}
	}
	return items, nil
}

func listOpItemsInVaultContext(ctx context.Context, vaultName string) ([]auditOpItem, error) {
	out, err := runOpCtxFn(ctx, "item", "list", "--vault", vaultName, "--format=json")
	if err != nil {
		return nil, err
	}
	var listed []struct {
		Title string `json:"title"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, fmt.Errorf("parse op item list for vault %s: %w", vaultName, err)
	}
	items := make([]auditOpItem, 0, len(listed))
	for _, item := range listed {
		if item.Title != "" {
			items = append(items, auditOpItem{ID: item.ID, Title: item.Title, Vault: vaultName})
		}
	}
	return items, nil
}

func listOpVaultNamesContext(ctx context.Context) ([]string, error) {
	out, err := runOpCtxFn(ctx, "vault", "list", "--format=json")
	if err != nil {
		return nil, err
	}
	var vaults []struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(out, &vaults); err != nil {
		return nil, fmt.Errorf("parse op vault list: %w", err)
	}
	names := make([]string, 0, len(vaults))
	for _, vault := range vaults {
		if vault.Name != "" {
			names = append(names, vault.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func renderAuditReport(report auditReport, output string) (string, error) {
	switch output {
	case "", "table":
		var b strings.Builder
		fmt.Fprintf(&b, "Summary: PASS=%d WARN=%d FAIL=%d\n", report.Summary.Pass, report.Summary.Warn, report.Summary.Fail)
		esRows := make([][]string, 0, len(report.ExternalSecrets))
		for _, es := range report.ExternalSecrets {
			esRows = append(esRows, []string{string(es.Status), es.ExternalSecret, es.Reason, es.Message})
		}
		b.WriteString("ExternalSecrets\n")
		b.WriteString(ui.Table([]string{"STATUS", "EXTERNALSECRET", "REASON", "MESSAGE"}, esRows))
		if len(report.MissingItems) > 0 {
			b.WriteString("\n\nMissing 1Password items\n")
			b.WriteString(ui.Table([]string{"STATUS", "ITEM", "REFERENCES", "DETAIL"}, auditItemRows(report.MissingItems)))
		}
		if len(report.OrphanItems) > 0 {
			b.WriteString("\n\nUnreferenced 1Password items\n")
			b.WriteString(ui.Table([]string{"STATUS", "ITEM", "VAULT", "DETAIL"}, auditOrphanRows(report.OrphanItems)))
		}
		return b.String(), nil
	case "json":
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(raw), nil
	default:
		return "", fmt.Errorf("unsupported output format %q (table, json)", output)
	}
}

func auditItemRows(items []auditItemFinding) [][]string {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{string(item.Status), item.Item, strings.Join(item.References, ", "), item.Detail})
	}
	return rows
}

func auditOrphanRows(items []auditItemFinding) [][]string {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		rows = append(rows, []string{string(item.Status), item.Item, item.Vault, item.Detail})
	}
	return rows
}
