package kubernetes

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
)

func newSuspendAppCommand() *cobra.Command {
	var namespace string
	var dryRun bool
	cmd := &cobra.Command{
		Use:          "suspend <app>",
		Short:        "Suspend an app for a maintenance window",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runK8sAppMaintenance("suspend", namespace, args[0], dryRun, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "application namespace (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print planned actions without mutating the cluster")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)
	return cmd
}

func newResumeAppCommand() *cobra.Command {
	var namespace string
	var dryRun bool
	cmd := &cobra.Command{
		Use:          "resume <app>",
		Short:        "Resume an app after a maintenance window",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runK8sAppMaintenance("resume", namespace, args[0], dryRun, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "application namespace (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print planned actions without mutating the cluster")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	_ = cmd.RegisterFlagCompletionFunc("app", completion.ValidApplications)
	return cmd
}

func runK8sAppMaintenance(action, namespace, app string, dryRun bool, out io.Writer) error {
	namespace = strings.TrimSpace(namespace)
	app = strings.TrimSpace(app)
	if namespace == "" {
		return fmt.Errorf("--namespace is required")
	}
	if app == "" {
		return fmt.Errorf("app is required")
	}
	if action != "suspend" && action != "resume" {
		return fmt.Errorf("unsupported maintenance action %q", action)
	}

	if dryRun {
		return printK8sMaintenanceDryRun(action, namespace, app, out)
	}
	if action == "suspend" {
		return suspendK8sApp(namespace, app)
	}
	return resumeK8sApp(namespace, app)
}

func printK8sMaintenanceDryRun(action, namespace, app string, out io.Writer) error {
	_, _ = fmt.Fprintf(out, "DRY-RUN %s %s/%s\n", strings.ToUpper(action), namespace, app)
	steps := dryRunMaintenanceSteps(action, namespace, app)
	for _, step := range steps {
		_, _ = fmt.Fprintln(out, step)
	}
	return nil
}

func dryRunMaintenanceSteps(action, namespace, app string) []string {
	if action == "resume" {
		return []string{
			fmt.Sprintf("kubectl --namespace %s patch replicationsource %s --type merge -p {\"spec\":{\"suspend\":false}}", namespace, app),
			fmt.Sprintf("flux --namespace %s resume kustomization %s", namespace, app),
			fmt.Sprintf("flux --namespace %s resume helmrelease %s", namespace, app),
			fmt.Sprintf("flux --namespace %s reconcile kustomization %s --with-source", namespace, app),
			fmt.Sprintf("flux --namespace %s reconcile helmrelease %s --force", namespace, app),
		}
	}
	return []string{
		fmt.Sprintf("flux --namespace %s suspend kustomization %s", namespace, app),
		fmt.Sprintf("flux --namespace %s suspend helmrelease %s", namespace, app),
		fmt.Sprintf("kubectl --namespace %s scale <detected-controller>/%s --replicas=0", namespace, app),
		fmt.Sprintf("kubectl --namespace %s patch replicationsource %s --type merge -p {\"spec\":{\"suspend\":true}}", namespace, app),
	}
}

func suspendK8sApp(namespace, app string) error {
	logger := common.NewColorLogger()
	logger.Info("Suspending %s/%s for maintenance", namespace, app)

	if err := commandRunFn("flux", "--namespace", namespace, "suspend", "kustomization", app); err != nil {
		return fmt.Errorf("failed to suspend kustomization %s/%s: %w", namespace, app, err)
	}
	if err := commandRunFn("flux", "--namespace", namespace, "suspend", "helmrelease", app); err != nil {
		return fmt.Errorf("failed to suspend helmrelease %s/%s: %w", namespace, app, err)
	}

	controller, err := detectK8sAppController(namespace, app)
	if err != nil {
		return err
	}
	if err := commandRunFn("kubectl", "--namespace", namespace, "scale", fmt.Sprintf("%s/%s", controller, app), "--replicas=0"); err != nil {
		return fmt.Errorf("failed to scale down %s/%s %s: %w", namespace, app, controller, err)
	}
	if err := patchReplicationSourceSuspend(namespace, app, true); err != nil {
		return err
	}
	logger.Success("Suspended %s/%s", namespace, app)
	return nil
}

func resumeK8sApp(namespace, app string) error {
	logger := common.NewColorLogger()
	logger.Info("Resuming %s/%s after maintenance", namespace, app)

	if err := patchReplicationSourceSuspend(namespace, app, false); err != nil {
		return err
	}
	if err := commandRunFn("flux", "--namespace", namespace, "resume", "kustomization", app); err != nil {
		return fmt.Errorf("failed to resume kustomization %s/%s: %w", namespace, app, err)
	}
	if err := commandRunFn("flux", "--namespace", namespace, "resume", "helmrelease", app); err != nil {
		return fmt.Errorf("failed to resume helmrelease %s/%s: %w", namespace, app, err)
	}
	if err := commandRunFn("flux", "--namespace", namespace, "reconcile", "kustomization", app, "--with-source"); err != nil {
		return fmt.Errorf("failed to reconcile kustomization %s/%s: %w", namespace, app, err)
	}
	if err := commandRunFn("flux", "--namespace", namespace, "reconcile", "helmrelease", app, "--force"); err != nil {
		return fmt.Errorf("failed to reconcile helmrelease %s/%s: %w", namespace, app, err)
	}
	logger.Success("Resumed %s/%s", namespace, app)
	return nil
}

func detectK8sAppController(namespace, app string) (string, error) {
	var lastErr error
	for _, controller := range []string{"deployment", "statefulset"} {
		if err := commandRunFn("kubectl", "--namespace", namespace, "get", controller, app); err == nil {
			return controller, nil
		} else {
			lastErr = err
		}
	}
	return "", fmt.Errorf("failed to detect deployment/statefulset for %s/%s: %w", namespace, app, lastErr)
}

func patchReplicationSourceSuspend(namespace, app string, suspend bool) error {
	patch := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
	if err := commandRunFn("kubectl", "--namespace", namespace, "patch", "replicationsource", app, "--type", "merge", "-p", patch); err != nil {
		return fmt.Errorf("failed to patch ReplicationSource %s/%s suspend=%t: %w", namespace, app, suspend, err)
	}
	return nil
}
