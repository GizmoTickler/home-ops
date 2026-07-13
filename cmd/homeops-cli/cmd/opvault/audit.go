package opvault

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
	"homeops-cli/internal/ui"
)

var opAuditKubectlOutputFn = func(args ...string) ([]byte, error) {
	return common.Output("kubectl", args...)
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
		Use:          "audit",
		Short:        "Audit ExternalSecrets against 1Password item inventory",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAudit(vault, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&vault, "vault", "all", "1Password vault to inspect, or all accessible vaults")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	return cmd
}

func runAudit(vault, output string, out io.Writer) error {
	report := buildAuditReport(vault)
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
	var report auditReport
	externalSecrets, references, refByItem, err := collectExternalSecretReferences()
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

	items, err := collectOpItems(vault)
	if err != nil {
		report.MissingItems = append(report.MissingItems, auditItemFinding{
			Item:   "1password-inventory",
			Status: auditFail,
			Detail: err.Error(),
		})
		report.finalize()
		return report
	}

	for item, refs := range refByItem {
		if _, ok := items[item]; !ok {
			report.MissingItems = append(report.MissingItems, auditItemFinding{
				Item:       item,
				Status:     auditFail,
				References: refs,
				Detail:     "referenced by ExternalSecret but missing from 1Password inventory",
			})
		}
	}
	for item, vaultName := range items {
		if _, ok := refByItem[item]; !ok {
			report.OrphanItems = append(report.OrphanItems, auditItemFinding{
				Item:   item,
				Vault:  vaultName,
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

func collectExternalSecretReferences() ([]auditExternalSecret, []auditReference, map[string][]string, error) {
	out, err := opAuditKubectlOutputFn("get", "externalsecrets.external-secrets.io", "-A", "-o", "json")
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

func collectOpItems(vault string) (map[string]string, error) {
	vaults := []string{vault}
	if strings.TrimSpace(vault) == "" || vault == "all" {
		list, err := listOpVaultNames()
		if err != nil {
			return nil, err
		}
		vaults = list
	}
	items := map[string]string{}
	for _, vaultName := range vaults {
		out, err := runOpFn("item", "list", "--vault", vaultName, "--format=json")
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
		for _, item := range listed {
			if item.Title != "" {
				items[item.Title] = vaultName
			}
		}
	}
	return items, nil
}

func listOpVaultNames() ([]string, error) {
	out, err := runOpFn("vault", "list", "--format=json")
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
