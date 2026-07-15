package volsync

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/kubeutil"
	"homeops-cli/internal/ui"
)

type verifyAllOptions struct {
	Namespace   string
	Skip        string
	Limit       int
	Timeout     time.Duration
	MaxDuration time.Duration
	Check       bool
	Output      string
}

type verifyAllResult string

const (
	verifyAllPass verifyAllResult = "PASS"
	verifyAllFail verifyAllResult = "FAIL"
	verifyAllSkip verifyAllResult = "SKIP"
)

type verifyAllRow struct {
	App       string          `json:"app"`
	Namespace string          `json:"namespace"`
	Result    verifyAllResult `json:"result"`
	Duration  string          `json:"duration"`
	Detail    string          `json:"detail"`
}

type verifyAllSummary struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

type verifyAllReport struct {
	Summary verifyAllSummary `json:"summary"`
	Results []verifyAllRow   `json:"results"`
}

type verifyAllFailuresError struct {
	count int
}

func (e verifyAllFailuresError) Error() string {
	return fmt.Sprintf("%d VolSync verification(s) failed", e.count)
}

var (
	verifyAllListFn   = listVerifyAllSources
	verifyAllRunOneFn = func(ctx context.Context, options verifyOptions) (verifyReport, error) {
		return executeVolsyncVerify(ctx, options, false)
	}
	verifyAllNowFn = time.Now
)

func newVerifyAllCommand() *cobra.Command {
	options := verifyAllOptions{}
	cmd := &cobra.Command{
		Use:          "verify-all",
		Short:        "Restore-verify a fleet of VolSync backups serially",
		SilenceUsage: true,
		Long: `Discovers ReplicationSources and runs the existing single-app restore
verification serially for each one. Every app keeps the same guaranteed scratch
resource cleanup as 'volsync verify'. Failures are aggregated, and apps not
started before --max-duration expires are reported as SKIP.`,
		Example: `  homeops-cli volsync verify-all --yes
  homeops-cli volsync verify-all --namespace media --skip plex,jellyfin --check --yes
  homeops-cli volsync verify-all --limit 3 --timeout 10m --max-duration 45m --output json --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runVolsyncVerifyAll(cmd.Context(), options, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&options.Namespace, "namespace", "n", "", "namespace to verify (default: all namespaces)")
	cmd.Flags().StringVar(&options.Skip, "skip", "", "comma-separated application names to skip")
	cmd.Flags().IntVar(&options.Limit, "limit", 0, "maximum number of applications to verify (0 means all)")
	cmd.Flags().DurationVar(&options.Timeout, "timeout", 15*time.Minute, "timeout for each application verification")
	cmd.Flags().DurationVar(&options.MaxDuration, "max-duration", 2*time.Hour, "total verification time budget")
	cmd.Flags().BoolVar(&options.Check, "check", false, "mount each scratch PVC read-only and verify it is non-empty")
	cmd.Flags().StringVarP(&options.Output, "output", "o", "table", "output format: table or json")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

func runVolsyncVerifyAll(ctx context.Context, options verifyAllOptions, out io.Writer) error {
	if err := ui.ValidateOutputFormat(options.Output); err != nil {
		return err
	}
	if options.Timeout <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}
	if options.MaxDuration <= 0 {
		return fmt.Errorf("max-duration must be greater than zero")
	}
	if options.Limit < 0 {
		return fmt.Errorf("limit must be zero or greater")
	}

	sources, err := verifyAllListFn(ctx, options.Namespace)
	if err != nil {
		return err
	}
	sources = filterVerifyAllSources(sources, options.Skip, options.Limit)
	if len(sources) > 0 {
		confirmed, confirmErr := confirmActionFn(verifyAllConfirmationMessage(sources, options.Check), false)
		if confirmErr != nil {
			return fmt.Errorf("confirmation failed: %w", confirmErr)
		}
		if !confirmed {
			return fmt.Errorf("verification cancelled")
		}
	}

	report := verifyAllReport{Results: make([]verifyAllRow, 0, len(sources))}
	started := verifyAllNowFn()
	deadline := started.Add(options.MaxDuration)
	budgetExpired := false
	for _, source := range sources {
		if budgetExpired || !verifyAllNowFn().Before(deadline) {
			budgetExpired = true
			report.Results = append(report.Results, verifyAllRow{
				App: source.Name, Namespace: source.Namespace, Result: verifyAllSkip,
				Duration: "0s", Detail: "total --max-duration budget exhausted before start",
			})
			continue
		}

		itemStarted := verifyAllNowFn()
		remaining := deadline.Sub(itemStarted)
		itemTimeout := options.Timeout
		if remaining < itemTimeout {
			itemTimeout = remaining
		}
		itemCtx, cancel := context.WithTimeout(ctx, itemTimeout)
		singleReport, runErr := verifyAllRunOneFn(itemCtx, verifyOptions{
			Namespace: source.Namespace,
			App:       source.Name,
			Timeout:   itemTimeout,
			Check:     options.Check,
			Output:    "table",
		})
		cancel()
		duration := verifyAllNowFn().Sub(itemStarted)
		if duration < 0 {
			duration = 0
		}
		row := verifyAllRow{App: source.Name, Namespace: source.Namespace, Duration: duration.Round(time.Millisecond).String()}
		if runErr != nil {
			row.Result = verifyAllFail
			row.Detail = compactVerifyAllDetail(runErr.Error())
		} else {
			row.Result = verifyAllPass
			row.Detail = "restored latest snapshot"
			if singleReport.IntegrityChecked {
				row.Detail += "; integrity check passed"
			}
		}
		report.Results = append(report.Results, row)
	}
	report.finalize()
	if err := writeVerifyAllReport(out, options.Output, report); err != nil {
		return err
	}
	if report.Summary.Fail > 0 {
		return verifyAllFailuresError{count: report.Summary.Fail}
	}
	return nil
}

func listVerifyAllSources(ctx context.Context, namespace string) ([]ReplicationSource, error) {
	var list replicationSourceList
	if err := kubeutil.GetJSON(ctx, verifyOutputFn, namespace, "replicationsources", &list); err != nil {
		return nil, fmt.Errorf("list ReplicationSources: %w", err)
	}
	sources := make([]ReplicationSource, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.Name == "" || item.Metadata.Namespace == "" {
			return nil, fmt.Errorf("ReplicationSource list contains an empty name or namespace")
		}
		sources = append(sources, ReplicationSource{Name: item.Metadata.Name, Namespace: item.Metadata.Namespace})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Namespace == sources[j].Namespace {
			return sources[i].Name < sources[j].Name
		}
		return sources[i].Namespace < sources[j].Namespace
	})
	return sources, nil
}

func filterVerifyAllSources(sources []ReplicationSource, skipText string, limit int) []ReplicationSource {
	skipped := map[string]struct{}{}
	for _, app := range strings.Split(skipText, ",") {
		if app = strings.TrimSpace(app); app != "" {
			skipped[app] = struct{}{}
		}
	}
	filtered := make([]ReplicationSource, 0, len(sources))
	for _, source := range sources {
		if _, skip := skipped[source.Name]; skip {
			continue
		}
		filtered = append(filtered, source)
		if limit > 0 && len(filtered) == limit {
			break
		}
	}
	return filtered
}

func verifyAllConfirmationMessage(sources []ReplicationSource, check bool) string {
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.Namespace+"/"+source.Name)
	}
	checkText := ""
	if check {
		checkText = " with integrity checks"
	}
	return fmt.Sprintf("Restore-verify %d VolSync application(s) serially%s, cleaning scratch resources after each: %s?",
		len(sources), checkText, strings.Join(names, ", "))
}

func compactVerifyAllDetail(detail string) string {
	return strings.Join(strings.Fields(detail), " ")
}

func (report *verifyAllReport) finalize() {
	report.Summary = verifyAllSummary{}
	for _, row := range report.Results {
		switch row.Result {
		case verifyAllPass:
			report.Summary.Pass++
		case verifyAllFail:
			report.Summary.Fail++
		case verifyAllSkip:
			report.Summary.Skip++
		}
	}
}

func writeVerifyAllReport(out io.Writer, output string, report verifyAllReport) error {
	if output == "json" {
		encoded, err := ui.RenderJSON(report)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, encoded)
		return err
	}
	rows := make([][]string, 0, len(report.Results))
	for _, row := range report.Results {
		rows = append(rows, []string{row.App, row.Namespace, string(row.Result), row.Duration, row.Detail})
	}
	if _, err := fmt.Fprintln(out, ui.Table([]string{"APP", "NAMESPACE", "RESULT", "DURATION", "DETAIL"}, rows)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "\nSummary: PASS=%d FAIL=%d SKIP=%d\n",
		report.Summary.Pass, report.Summary.Fail, report.Summary.Skip)
	return err
}
