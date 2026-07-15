package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ssh"
	"homeops-cli/internal/ui"
)

const (
	certDefaultWarnDays  = 30
	certManifestMoveWait = 20 * time.Second
)

type certStatus string

const (
	certOK   certStatus = "OK"
	certWarn certStatus = "WARN"
	certFail certStatus = "FAIL"
)

type certCheck struct {
	Node              string     `json:"node"`
	Name              string     `json:"name"`
	Expiry            string     `json:"expiry,omitempty"`
	ResidualDays      *int       `json:"residual_days,omitempty"`
	CAName            string     `json:"ca_name,omitempty"`
	CertificateAuth   bool       `json:"certificate_authority,omitempty"`
	ExternallyManaged bool       `json:"externally_managed"`
	Status            certStatus `json:"status"`
	Detail            string     `json:"detail,omitempty"`
}

type certReport struct {
	Checks []certCheck `json:"checks"`
	OK     int         `json:"ok"`
	Warn   int         `json:"warn"`
	Fail   int         `json:"fail"`
}

type certWorkflowReport struct {
	Before certReport  `json:"before"`
	After  *certReport `json:"after,omitempty"`
}

type parsedKubeadmCert struct {
	Name              string
	Expiry            time.Time
	HasExpiry         bool
	CAName            string
	CertificateAuth   bool
	ExternallyManaged bool
	Missing           bool
}

var (
	certNowFn         = time.Now
	certNodeCommandFn = func(_ context.Context, node config.Node, sshUser, command string) (string, error) {
		client := ssh.NewSSHClient(kubeutil.NodeSSHConfig(node, sshUser))
		if err := client.Connect(); err != nil {
			return "", err
		}
		defer func() { _ = client.Close() }()
		return client.ExecuteCommand(command)
	}
	certWaitFn = func(ctx context.Context, duration time.Duration) error {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
)

func newCertsCommand() *cobra.Command {
	var output string
	var warnDays int
	var failOnWarn, renew, restartControlPlane bool
	cmd := &cobra.Command{
		Use:          "certs",
		Short:        "Check or renew kubeadm control-plane certificates",
		SilenceUsage: true,
		Long: `Check kubeadm-managed PKI expiration on every configured control-plane node.
Renewal modifies node PKI and is confirmation-gated. Static control-plane pods
must be restarted after renewal before they use the new certificates.`,
		Example: `  homeops-cli k8s certs
  homeops-cli k8s certs --warn-days 45 --fail-on-warn --output json
  homeops-cli k8s certs --renew
  homeops-cli k8s certs --renew --restart-control-plane --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ui.ValidateOutputFormat(output); err != nil {
				return err
			}
			if warnDays < 0 {
				return fmt.Errorf("--warn-days cannot be negative")
			}
			if restartControlPlane && !renew {
				return fmt.Errorf("--restart-control-plane requires --renew")
			}
			report, changed, err := runCertsWorkflow(cmd.Context(), warnDays, renew, restartControlPlane)
			if err != nil {
				if changed {
					printCertRestartNote(cmd.ErrOrStderr())
				}
				return err
			}
			rendered, err := renderCertWorkflow(report, output)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			if changed {
				printCertRestartNote(cmd.ErrOrStderr())
			}
			active := report.Before
			if report.After != nil {
				active = *report.After
			}
			return certReportExitError(active, failOnWarn)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().IntVar(&warnDays, "warn-days", certDefaultWarnDays, "warn when a certificate expires in fewer than this many days")
	cmd.Flags().BoolVar(&failOnWarn, "fail-on-warn", false, "return exit code 1 when any certificate is WARN")
	cmd.Flags().BoolVar(&renew, "renew", false, "renew all kubeadm-managed certificates on every control-plane node (destructive)")
	cmd.Flags().BoolVar(&restartControlPlane, "restart-control-plane", false, "restart static control-plane pods one component at a time after renewal (destructive)")
	return cmd
}

func runCertsWorkflow(ctx context.Context, warnDays int, renew, restart bool) (certWorkflowReport, bool, error) {
	cfg := config.Get()
	sshUser, err := cfg.ResolveSecret(config.KeyNodeSSHUser)
	if err != nil {
		return certWorkflowReport{}, false, fmt.Errorf("resolve node SSH user: %w", err)
	}
	sshUser = strings.TrimSpace(sshUser)
	if sshUser == "" {
		return certWorkflowReport{}, false, fmt.Errorf("resolved node SSH user is empty")
	}
	nodes := cfg.Cluster.Nodes
	if len(nodes) == 0 {
		return certWorkflowReport{}, false, fmt.Errorf("cluster.nodes has no control-plane nodes")
	}
	before := inspectKubeadmCerts(ctx, nodes, sshUser, warnDays)
	workflow := certWorkflowReport{Before: before}
	if !renew {
		return workflow, false, nil
	}

	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.Name)
	}
	confirmed, err := confirmActionFn(fmt.Sprintf("DESTRUCTIVE: renew all kubeadm-managed certificates on control-plane nodes %s?", strings.Join(names, ", ")), false)
	if err != nil {
		return workflow, false, err
	}
	if !confirmed {
		return workflow, false, nil
	}
	for _, node := range nodes {
		if _, err := certNodeCommandFn(ctx, node, sshUser, "sudo kubeadm certs renew all"); err != nil {
			return workflow, true, fmt.Errorf("renew kubeadm certificates on %s: %w", node.Name, err)
		}
	}

	if restart {
		confirmed, err := confirmActionFn(fmt.Sprintf("DESTRUCTIVE: restart kube-apiserver, kube-controller-manager, kube-scheduler, and etcd static pods on %s one component at a time?", strings.Join(names, ", ")), false)
		if err != nil {
			return workflow, true, err
		}
		if !confirmed {
			restart = false
		}
		if restart {
			if err := restartControlPlaneStaticPods(ctx, nodes, sshUser); err != nil {
				return workflow, true, err
			}
		}
	}
	after := inspectKubeadmCerts(ctx, nodes, sshUser, warnDays)
	workflow.After = &after
	return workflow, true, nil
}

func inspectKubeadmCerts(ctx context.Context, nodes []config.Node, sshUser string, warnDays int) certReport {
	report := certReport{}
	for _, node := range nodes {
		output, err := certNodeCommandFn(ctx, node, sshUser, "sudo kubeadm certs check-expiration -o json")
		var parsed []parsedKubeadmCert
		if err == nil {
			parsed, err = parseKubeadmCertsJSON([]byte(output))
		}
		if err != nil {
			textOutput, textErr := certNodeCommandFn(ctx, node, sshUser, "sudo kubeadm certs check-expiration")
			if textErr != nil {
				report.Checks = append(report.Checks, certCheck{
					Node: node.Name, Name: "kubeadm certs check-expiration", Status: certFail,
					Detail: fmt.Sprintf("JSON check failed: %v; text fallback failed: %v", err, textErr),
				})
				continue
			}
			parsed, err = parseKubeadmCertsText(textOutput)
			if err != nil {
				report.Checks = append(report.Checks, certCheck{Node: node.Name, Name: "kubeadm certs check-expiration", Status: certFail, Detail: err.Error()})
				continue
			}
		}
		for _, certificate := range parsed {
			report.Checks = append(report.Checks, classifyKubeadmCert(node.Name, certificate, certNowFn(), warnDays))
		}
	}
	sort.SliceStable(report.Checks, func(i, j int) bool {
		if report.Checks[i].Node == report.Checks[j].Node {
			if report.Checks[i].CertificateAuth == report.Checks[j].CertificateAuth {
				return report.Checks[i].Name < report.Checks[j].Name
			}
			return !report.Checks[i].CertificateAuth
		}
		return report.Checks[i].Node < report.Checks[j].Node
	})
	for _, check := range report.Checks {
		switch check.Status {
		case certFail:
			report.Fail++
		case certWarn:
			report.Warn++
		default:
			report.OK++
		}
	}
	return report
}

func parseKubeadmCertsJSON(raw []byte) ([]parsedKubeadmCert, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse kubeadm certificate JSON: %w", err)
	}
	type jsonCert struct {
		Name              string `json:"name"`
		ExpirationDate    string `json:"expirationDate"`
		CAName            string `json:"caName"`
		ExternallyManaged bool   `json:"externallyManaged"`
		Missing           bool   `json:"missing"`
	}
	groups := []struct {
		keys []string
		isCA bool
	}{
		{keys: []string{"certificateExpirationInfo", "certificates"}},
		{keys: []string{"caExpirationInfo", "certificateAuthorities"}, isCA: true},
	}
	var out []parsedKubeadmCert
	for _, group := range groups {
		for _, key := range group.keys {
			value, ok := root[key]
			if !ok {
				continue
			}
			var entries []jsonCert
			if err := json.Unmarshal(value, &entries); err != nil {
				return nil, fmt.Errorf("parse kubeadm certificate JSON field %s: %w", key, err)
			}
			for _, entry := range entries {
				certificate := parsedKubeadmCert{
					Name: entry.Name, CAName: entry.CAName, CertificateAuth: group.isCA,
					ExternallyManaged: entry.ExternallyManaged, Missing: entry.Missing,
				}
				if strings.TrimSpace(entry.ExpirationDate) != "" {
					expires, err := parseKubeadmTime(entry.ExpirationDate)
					if err != nil {
						return nil, fmt.Errorf("parse expiry for %s: %w", entry.Name, err)
					}
					certificate.Expiry, certificate.HasExpiry = expires, true
				}
				out = append(out, certificate)
			}
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kubeadm certificate JSON contains no certificate expiration entries")
	}
	return out, nil
}

var textColumns = regexp.MustCompile(`\s{2,}`)

func parseKubeadmCertsText(raw string) ([]parsedKubeadmCert, error) {
	var out []parsedKubeadmCert
	isCA := false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, "CERTIFICATE AUTHORITY") {
			isCA = true
			continue
		}
		if strings.HasPrefix(upper, "CERTIFICATE") {
			isCA = false
			continue
		}
		fields := textColumns.Split(line, -1)
		if strings.HasPrefix(upper, "!MISSING!") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "!MISSING!"))
			if name != "" {
				out = append(out, parsedKubeadmCert{Name: name, CertificateAuth: isCA, Missing: true})
			}
			continue
		}
		if len(fields) < 2 {
			continue
		}
		certificate := parsedKubeadmCert{Name: strings.TrimSpace(fields[0]), CertificateAuth: isCA}
		last := strings.TrimSpace(fields[len(fields)-1])
		certificate.ExternallyManaged = strings.EqualFold(last, "yes") || strings.EqualFold(last, "true")
		expires, err := parseKubeadmTime(fields[1])
		if err != nil {
			if certificate.ExternallyManaged && strings.Contains(strings.ToUpper(fields[1]), "EXTERNAL") {
				out = append(out, certificate)
				continue
			}
			return nil, fmt.Errorf("parse text expiry for %s from %q: %w", certificate.Name, fields[1], err)
		}
		certificate.Expiry, certificate.HasExpiry = expires, true
		if !isCA && len(fields) >= 4 {
			certificate.CAName = strings.TrimSpace(fields[3])
			if certificate.CAName == "<none>" {
				certificate.CAName = ""
			}
		}
		out = append(out, certificate)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("kubeadm certificate text contains no expiration rows")
	}
	return out, nil
}

func parseKubeadmTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	layouts := []string{
		time.RFC3339Nano,
		"Jan 2, 2006 15:04 MST",
		"Jan 02, 2006 15:04 MST",
		"2006-01-02 15:04:05 MST",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func classifyKubeadmCert(node string, certificate parsedKubeadmCert, now time.Time, warnDays int) certCheck {
	check := certCheck{
		Node: node, Name: certificate.Name, CAName: certificate.CAName,
		CertificateAuth: certificate.CertificateAuth, ExternallyManaged: certificate.ExternallyManaged,
		Status: certOK,
	}
	if certificate.Missing {
		// kubeadm >=1.29 creates super-admin.conf only on the init node;
		// joined control-plane nodes report it as missing. Anything else
		// missing is a real problem.
		if certificate.Name == "super-admin.conf" {
			check.Detail = "not present on this node (only the kubeadm init node has it)"
		} else {
			check.Status, check.Detail = certFail, "reported missing by kubeadm"
		}
		return check
	}
	if certificate.HasExpiry {
		check.Expiry = certificate.Expiry.UTC().Format(time.RFC3339)
		remaining := certificate.Expiry.Sub(now)
		residualDays := int(math.Floor(remaining.Hours() / 24))
		check.ResidualDays = &residualDays
		switch {
		case remaining <= 0:
			check.Status, check.Detail = certFail, "expired"
		case remaining < time.Duration(warnDays)*24*time.Hour:
			check.Status, check.Detail = certWarn, fmt.Sprintf("expires in fewer than %d days", warnDays)
		default:
			check.Detail = "valid"
		}
	} else if certificate.ExternallyManaged {
		check.Detail = "externally managed; kubeadm does not report an expiry"
	} else {
		check.Status, check.Detail = certFail, "expiry not reported"
	}
	if certificate.ExternallyManaged && certificate.HasExpiry {
		check.Detail += "; externally managed"
	}
	return check
}

func certReportExitError(report certReport, failOnWarn bool) error {
	if report.Fail > 0 {
		return fmt.Errorf("kubeadm certificate check found %d expired or failed certificate check(s)", report.Fail)
	}
	if failOnWarn && report.Warn > 0 {
		return fmt.Errorf("kubeadm certificate check found %d warning(s) and --fail-on-warn is set", report.Warn)
	}
	return nil
}

func restartControlPlaneStaticPods(ctx context.Context, nodes []config.Node, sshUser string) error {
	components := []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler", "etcd"}
	for _, node := range nodes {
		for _, component := range components {
			manifest := "/etc/kubernetes/manifests/" + component + ".yaml"
			parked := "/var/tmp/homeops-" + component + ".yaml"
			moveOut := fmt.Sprintf("sudo test ! -e %s && sudo mv -- %s %s", common.ShellQuote(parked), common.ShellQuote(manifest), common.ShellQuote(parked))
			moveBack := fmt.Sprintf("sudo mv -- %s %s", common.ShellQuote(parked), common.ShellQuote(manifest))
			if _, err := certNodeCommandFn(ctx, node, sshUser, moveOut); err != nil {
				return fmt.Errorf("move %s static pod manifest out on %s: %w", component, node.Name, err)
			}
			if err := certWaitFn(ctx, certManifestMoveWait); err != nil {
				_, _ = certNodeCommandFn(context.WithoutCancel(ctx), node, sshUser, moveBack)
				return fmt.Errorf("wait after stopping %s on %s: %w", component, node.Name, err)
			}
			if _, err := certNodeCommandFn(context.WithoutCancel(ctx), node, sshUser, moveBack); err != nil {
				return fmt.Errorf("restore %s static pod manifest on %s: %w", component, node.Name, err)
			}
			if err := certWaitFn(ctx, certManifestMoveWait); err != nil {
				return fmt.Errorf("wait after starting %s on %s: %w", component, node.Name, err)
			}
		}
	}
	return nil
}

func renderCertWorkflow(workflow certWorkflowReport, output string) (string, error) {
	switch output {
	case "table":
		if workflow.After == nil {
			return renderCertReportTable(workflow.Before), nil
		}
		return "Before renewal\n" + renderCertReportTable(workflow.Before) + "\n\nAfter renewal\n" + renderCertReportTable(*workflow.After), nil
	case "json":
		return ui.RenderJSON(workflow)
	default:
		return "", ui.ValidateOutputFormat(output)
	}
}

func renderCertReportTable(report certReport) string {
	rows := make([][]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		days := "-"
		if check.ResidualDays != nil {
			days = strconv.Itoa(*check.ResidualDays)
		}
		managed := "kubeadm"
		if check.ExternallyManaged {
			managed = "external"
		}
		kind := "certificate"
		if check.CertificateAuth {
			kind = "CA"
		}
		rows = append(rows, []string{
			check.Node, string(check.Status), kind, check.Name, check.Expiry, days, check.CAName, managed, check.Detail,
		})
	}
	summary := fmt.Sprintf("Summary: OK=%d WARN=%d FAIL=%d", report.OK, report.Warn, report.Fail)
	return summary + "\n" + ui.Table([]string{"NODE", "STATUS", "KIND", "CERTIFICATE", "EXPIRES", "DAYS", "CA", "MANAGED BY", "DETAIL"}, rows)
}

func printCertRestartNote(out io.Writer) {
	_, _ = fmt.Fprintln(out, "IMPORTANT: renewed certificates are not active until kube-apiserver, kube-controller-manager, kube-scheduler, and etcd static pods have restarted on every control-plane node.")
}
