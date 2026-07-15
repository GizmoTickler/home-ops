package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"homeops-cli/cmd/completion"
	"homeops-cli/internal/common"
	"homeops-cli/internal/config"
	"homeops-cli/internal/ui"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	rightSizeDefaultWindow = 7 * 24 * time.Hour
	rightSizeVMPort        = 8429
	rightSizeCPUOverGap    = 0.1
	rightSizeMemoryOverGap = 128 * 1024 * 1024
)

type rightSizeVerdict string

const (
	rightSizeOver  rightSizeVerdict = "OVERPROVISIONED"
	rightSizeUnder rightSizeVerdict = "UNDERPROVISIONED"
	rightSizeOK    rightSizeVerdict = "OK"
)

type rightSizeContainer struct {
	Namespace              string           `json:"namespace"`
	WorkloadKind           string           `json:"workload_kind"`
	Workload               string           `json:"workload"`
	Container              string           `json:"container"`
	CPURequestCores        float64          `json:"cpu_request_cores"`
	CPUP95Cores            float64          `json:"cpu_p95_cores"`
	CPURatio               *float64         `json:"cpu_request_to_p95_ratio,omitempty"`
	MemoryRequestBytes     float64          `json:"memory_request_bytes"`
	MemoryP95Bytes         float64          `json:"memory_p95_bytes"`
	MemoryMaxBytes         float64          `json:"memory_max_bytes"`
	MemoryRatio            *float64         `json:"memory_request_to_p95_ratio,omitempty"`
	MemoryLimitBytes       float64          `json:"memory_limit_bytes,omitempty"`
	SuggestedCPUCores      float64          `json:"suggested_cpu_request_cores"`
	SuggestedMemoryBytes   float64          `json:"suggested_memory_request_bytes"`
	EstimatedSavingsPct    float64          `json:"estimated_savings_percent"`
	Verdict                rightSizeVerdict `json:"verdict"`
	PointInTimeObservation bool             `json:"point_in_time"`
	score                  float64
}

type rightSizeReport struct {
	Source      string               `json:"source"`
	Window      string               `json:"window"`
	PointInTime bool                 `json:"point_in_time"`
	Warning     string               `json:"warning,omitempty"`
	Containers  []rightSizeContainer `json:"containers"`
}

type rightSizeMetricSample struct {
	Namespace string
	Pod       string
	Container string
	Value     float64
}

type rightSizePromResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

type rightSizeServiceList struct {
	Items []struct {
		Metadata metadataJSON `json:"metadata"`
		Spec     struct {
			Ports []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	} `json:"items"`
}

type rightSizeOwnerReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type rightSizePodList struct {
	Items []struct {
		Metadata struct {
			Name            string                    `json:"name"`
			Namespace       string                    `json:"namespace"`
			OwnerReferences []rightSizeOwnerReference `json:"ownerReferences"`
		} `json:"metadata"`
		Spec struct {
			Containers []struct {
				Name      string `json:"name"`
				Resources struct {
					Requests map[string]string `json:"requests"`
					Limits   map[string]string `json:"limits"`
				} `json:"resources"`
			} `json:"containers"`
		} `json:"spec"`
	} `json:"items"`
}

type rightSizeReplicaSetList struct {
	Items []struct {
		Metadata struct {
			Name            string                    `json:"name"`
			Namespace       string                    `json:"namespace"`
			OwnerReferences []rightSizeOwnerReference `json:"ownerReferences"`
		} `json:"metadata"`
	} `json:"items"`
}

type rightSizeWorkloadRef struct {
	Namespace string
	Kind      string
	Name      string
}

type rightSizeResource struct {
	Workload    rightSizeWorkloadRef
	Container   string
	CPURequest  float64
	MemoryReq   float64
	MemoryLimit float64
}

type rightSizeObservation struct {
	Resource  rightSizeResource
	CPUP95    float64
	MemoryP95 float64
	MemoryMax float64
	Observed  bool
}

var (
	rightSizeConfigFn        = config.Get
	rightSizeKubectlOutputFn = func(ctx context.Context, args ...string) ([]byte, error) {
		return kubectlOutputCtxFn(ctx, args...)
	}
	rightSizePortForwardFn = startRightSizePortForward
	rightSizePromQueryFn   = queryRightSizePrometheus
)

func newRightSizeCommand() *cobra.Command {
	var namespace, output string
	var windowText string
	var minSavings float64
	cmd := &cobra.Command{
		Use:          "right-size",
		Short:        "Find over- and under-provisioned workload containers",
		SilenceUsage: true,
		Example: `  homeops-cli k8s right-size
  homeops-cli k8s right-size --namespace media --window 14d
  homeops-cli k8s right-size --min-savings 25 --output json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "table" && output != "json" {
				return fmt.Errorf("unsupported output format %q (table, json)", output)
			}
			window, err := parseRightSizeWindow(windowText)
			if err != nil {
				return err
			}
			if window < 5*time.Minute {
				return fmt.Errorf("--window must be at least 5m")
			}
			if minSavings < 0 || minSavings > 100 {
				return fmt.Errorf("--min-savings must be between 0 and 100")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), kubernetesDefaultCommandTimeout)
			defer cancel()
			report, err := buildRightSizeReport(ctx, namespace, window, minSavings)
			if err != nil {
				return err
			}
			rendered, err := renderRightSizeReport(report, output)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), rendered)
			return nil // Advisory command: findings never change the exit code.
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to inspect (default: all namespaces)")
	cmd.Flags().StringVar(&windowText, "window", "7d", "VictoriaMetrics lookback window (for example 24h or 7d)")
	cmd.Flags().StringVarP(&output, "output", "o", "table", "output format: table or json")
	cmd.Flags().Float64Var(&minSavings, "min-savings", 0, "hide overprovisioned rows below this estimated savings percentage")
	_ = cmd.RegisterFlagCompletionFunc("namespace", completion.ValidNamespaces)
	return cmd
}

func buildRightSizeReport(ctx context.Context, namespace string, window time.Duration, minSavings float64) (rightSizeReport, error) {
	resources, podWorkloads, err := loadRightSizeResources(ctx, namespace)
	if err != nil {
		return rightSizeReport{}, err
	}
	report, vmErr := buildVictoriaMetricsRightSizeReport(ctx, namespace, window, resources, podWorkloads)
	if vmErr != nil {
		report, err = buildMetricsAPIRightSizeReport(ctx, namespace, resources, podWorkloads)
		if err != nil {
			return rightSizeReport{}, fmt.Errorf("VictoriaMetrics unavailable (%v); metrics.k8s.io fallback failed: %w", vmErr, err)
		}
		report.Warning = "WARN: VictoriaMetrics unavailable; using instantaneous metrics.k8s.io data. Results are point-in-time, not historical p95/max values."
	}
	report.Containers = finalizeRightSizeObservations(report.Containers, minSavings)
	return report, nil
}

func loadRightSizeResources(ctx context.Context, namespace string) (map[string]rightSizeResource, map[string]rightSizeWorkloadRef, error) {
	var pods rightSizePodList
	if err := kubectlGetJSONWithSeam(ctx, namespace, "pods", &pods); err != nil {
		return nil, nil, err
	}
	var replicaSets rightSizeReplicaSetList
	if err := kubectlGetJSONWithSeam(ctx, namespace, "replicasets.apps", &replicaSets); err != nil {
		return nil, nil, err
	}
	replicaOwners := make(map[string]rightSizeWorkloadRef, len(replicaSets.Items))
	for _, replicaSet := range replicaSets.Items {
		ref := rightSizeWorkloadRef{Namespace: replicaSet.Metadata.Namespace, Kind: "ReplicaSet", Name: replicaSet.Metadata.Name}
		if len(replicaSet.Metadata.OwnerReferences) > 0 {
			ref.Kind = replicaSet.Metadata.OwnerReferences[0].Kind
			ref.Name = replicaSet.Metadata.OwnerReferences[0].Name
		}
		replicaOwners[rightSizePodKey(replicaSet.Metadata.Namespace, replicaSet.Metadata.Name)] = ref
	}
	resources := map[string]rightSizeResource{}
	podWorkloads := map[string]rightSizeWorkloadRef{}
	for _, pod := range pods.Items {
		workload := rightSizeWorkloadRef{Namespace: pod.Metadata.Namespace, Kind: "Pod", Name: pod.Metadata.Name}
		if len(pod.Metadata.OwnerReferences) > 0 {
			owner := pod.Metadata.OwnerReferences[0]
			workload.Kind, workload.Name = owner.Kind, owner.Name
			if owner.Kind == "ReplicaSet" {
				if resolved, ok := replicaOwners[rightSizePodKey(pod.Metadata.Namespace, owner.Name)]; ok {
					workload = resolved
				}
			}
		}
		podWorkloads[rightSizePodKey(pod.Metadata.Namespace, pod.Metadata.Name)] = workload
		for _, container := range pod.Spec.Containers {
			key := rightSizeMetricKey(pod.Metadata.Namespace, pod.Metadata.Name, container.Name)
			resources[key] = rightSizeResource{
				Workload: workload, Container: container.Name,
				CPURequest:  parseRightSizeQuantity(container.Resources.Requests["cpu"], true),
				MemoryReq:   parseRightSizeQuantity(container.Resources.Requests["memory"], false),
				MemoryLimit: parseRightSizeQuantity(container.Resources.Limits["memory"], false),
			}
		}
	}
	return resources, podWorkloads, nil
}

func kubectlGetJSONWithSeam(ctx context.Context, namespace, resourceName string, dest any) error {
	args := scopedKubectlGetArgs(namespace, resourceName)
	raw, err := rightSizeKubectlOutputFn(ctx, args...)
	if err != nil {
		return fmt.Errorf("kubectl get %s: %w", resourceName, err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("parse kubectl %s json: %w", resourceName, err)
	}
	return nil
}

func buildVictoriaMetricsRightSizeReport(ctx context.Context, namespace string, window time.Duration, resources map[string]rightSizeResource, podWorkloads map[string]rightSizeWorkloadRef) (rightSizeReport, error) {
	service, port, err := discoverVictoriaMetricsService(ctx)
	if err != nil {
		return rightSizeReport{}, err
	}
	baseURL, stop, err := rightSizePortForwardFn(ctx, rightSizeConfigFn().Cluster.Observability.Namespace, service, port)
	if err != nil {
		return rightSizeReport{}, err
	}
	defer func() { _ = stop() }()
	if strings.HasPrefix(strings.ToLower(service), "vmselect") {
		baseURL = strings.TrimRight(baseURL, "/") + "/select/0/prometheus"
	}

	selector := `container!="",pod!=""`
	if namespace != "" {
		selector += `,namespace="` + namespace + `"`
	}
	windowText := prometheusDuration(window)
	queries := []struct {
		name  string
		query string
	}{
		{"cpu", fmt.Sprintf(`max by (namespace,pod,container) (quantile_over_time(0.95, rate(container_cpu_usage_seconds_total{%s}[5m])[%s:5m]))`, selector, windowText)},
		{"memory_p95", fmt.Sprintf(`max by (namespace,pod,container) (quantile_over_time(0.95, container_memory_working_set_bytes{%s}[%s:5m]))`, selector, windowText)},
		{"memory_max", fmt.Sprintf(`max by (namespace,pod,container) (max_over_time(container_memory_working_set_bytes{%s}[%s:5m]))`, selector, windowText)},
		{"cpu_request", rightSizeResourcePromQL("kube_pod_container_resource_requests", "cpu", namespace)},
		{"memory_request", rightSizeResourcePromQL("kube_pod_container_resource_requests", "memory", namespace)},
		{"memory_limit", rightSizeResourcePromQL("kube_pod_container_resource_limits", "memory", namespace)},
	}
	observations := map[string]*rightSizeObservation{}
	for key, resourceValue := range resources {
		copyValue := resourceValue
		observations[key] = &rightSizeObservation{Resource: copyValue}
	}
	for _, query := range queries {
		raw, queryErr := rightSizePromQueryFn(ctx, baseURL, query.query)
		if queryErr != nil {
			return rightSizeReport{}, fmt.Errorf("VictoriaMetrics %s query: %w", query.name, queryErr)
		}
		samples, parseErr := parseRightSizePromQLResponse(raw)
		if parseErr != nil {
			return rightSizeReport{}, fmt.Errorf("VictoriaMetrics %s response: %w", query.name, parseErr)
		}
		for _, sample := range samples {
			key := rightSizeMetricKey(sample.Namespace, sample.Pod, sample.Container)
			observation, ok := observations[key]
			if !ok {
				workload, exists := podWorkloads[rightSizePodKey(sample.Namespace, sample.Pod)]
				if !exists {
					continue
				}
				observation = &rightSizeObservation{Resource: rightSizeResource{Workload: workload, Container: sample.Container}}
				observations[key] = observation
			}
			switch query.name {
			case "cpu":
				observation.CPUP95 = sample.Value
				observation.Observed = true
			case "memory_p95":
				observation.MemoryP95 = sample.Value
				observation.Observed = true
			case "memory_max":
				observation.MemoryMax = sample.Value
				observation.Observed = true
			case "cpu_request":
				observation.Resource.CPURequest = sample.Value
			case "memory_request":
				observation.Resource.MemoryReq = sample.Value
			case "memory_limit":
				observation.Resource.MemoryLimit = sample.Value
			}
		}
	}
	observed := false
	for _, observation := range observations {
		observed = observed || observation.Observed
	}
	if !observed {
		return rightSizeReport{}, fmt.Errorf("VictoriaMetrics returned no container usage samples")
	}
	return rightSizeReport{Source: "victoriametrics", Window: window.String(), Containers: aggregateRightSizeObservations(observations, false)}, nil
}

func discoverVictoriaMetricsService(ctx context.Context) (string, int, error) {
	namespace := strings.TrimSpace(rightSizeConfigFn().Cluster.Observability.Namespace)
	if namespace == "" {
		return "", 0, fmt.Errorf("cluster.observability.namespace is empty")
	}
	var services rightSizeServiceList
	if err := kubectlGetJSONWithSeam(ctx, namespace, "services", &services); err != nil {
		return "", 0, err
	}
	type candidate struct {
		name string
		port int
	}
	var candidates []candidate
	for _, service := range services.Items {
		name := strings.ToLower(service.Metadata.Name)
		if !strings.HasPrefix(name, "vmsingle") && !strings.HasPrefix(name, "vmselect") {
			continue
		}
		for _, port := range service.Spec.Ports {
			if strings.EqualFold(port.Name, "http") || port.Port == rightSizeVMPort {
				candidates = append(candidates, candidate{service.Metadata.Name, port.Port})
			}
		}
	}
	if len(candidates) == 0 {
		return "", 0, fmt.Errorf("no vmsingle*/vmselect* Service with an http/%d port found in namespace %s", rightSizeVMPort, namespace)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].name < candidates[j].name })
	return candidates[0].name, candidates[0].port, nil
}

func startRightSizePortForward(ctx context.Context, namespace, service string, remotePort int) (string, func() error, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("reserve local port: %w", err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return "", nil, fmt.Errorf("release reserved local port: %w", err)
	}
	forwardCtx, cancel := context.WithCancel(ctx)
	var stdout, stderr bytes.Buffer
	cmd := common.CommandWithContext(forwardCtx, "kubectl", "port-forward", "--namespace", namespace, "service/"+service,
		fmt.Sprintf("%d:%d", localPort, remotePort), "--address", "127.0.0.1")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	waitCh := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, fmt.Errorf("start kubectl port-forward: %w", err)
	}
	go func() { waitCh <- cmd.Wait() }()

	var once sync.Once
	stop := func() error {
		cancel()
		var waitErr error
		once.Do(func() { waitErr = <-waitCh })
		if waitErr != nil && ctx.Err() == nil && !strings.Contains(waitErr.Error(), "signal: killed") {
			return waitErr
		}
		return nil
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, dialErr := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return "http://" + address, stop, nil
		}
		select {
		case waitErr := <-waitCh:
			once.Do(func() {})
			cancel()
			detail := strings.TrimSpace(common.RedactCommandOutput(stderr.String() + "\n" + stdout.String()))
			if detail != "" {
				return "", nil, fmt.Errorf("kubectl port-forward exited: %w: %s", waitErr, detail)
			}
			return "", nil, fmt.Errorf("kubectl port-forward exited: %w", waitErr)
		case <-ctx.Done():
			_ = stop()
			return "", nil, ctx.Err()
		case <-deadline.C:
			_ = stop()
			return "", nil, fmt.Errorf("timed out waiting for kubectl port-forward")
		case <-ticker.C:
		}
	}
}

func queryRightSizePrometheus(ctx context.Context, baseURL, query string) ([]byte, error) {
	values := url.Values{"query": []string{query}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v1/query?"+values.Encode(), nil) // #nosec G107 -- baseURL is a loopback port-forward returned by this process.
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func parseRightSizePromQLResponse(raw []byte) ([]rightSizeMetricSample, error) {
	var response rightSizePromResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if response.Status != "success" {
		return nil, fmt.Errorf("query status %q: %s", response.Status, response.Error)
	}
	if response.Data.ResultType != "vector" {
		return nil, fmt.Errorf("unexpected result type %q", response.Data.ResultType)
	}
	samples := make([]rightSizeMetricSample, 0, len(response.Data.Result))
	for _, result := range response.Data.Result {
		if len(result.Value) != 2 {
			return nil, fmt.Errorf("vector sample has %d value fields", len(result.Value))
		}
		var valueText string
		if err := json.Unmarshal(result.Value[1], &valueText); err != nil {
			return nil, fmt.Errorf("parse sample value: %w", err)
		}
		value, err := strconv.ParseFloat(valueText, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("invalid sample value %q", valueText)
		}
		samples = append(samples, rightSizeMetricSample{
			Namespace: result.Metric["namespace"], Pod: result.Metric["pod"], Container: result.Metric["container"], Value: value,
		})
	}
	return samples, nil
}

func buildMetricsAPIRightSizeReport(ctx context.Context, namespace string, resources map[string]rightSizeResource, _ map[string]rightSizeWorkloadRef) (rightSizeReport, error) {
	args := []string{"top", "pods", "--containers", "--no-headers"}
	if namespace == "" {
		args = append(args, "-A")
	} else {
		args = append(args, "--namespace", namespace)
	}
	raw, err := rightSizeKubectlOutputFn(ctx, args...)
	if err != nil {
		return rightSizeReport{}, err
	}
	samples, err := parseKubectlTopContainers(raw, namespace)
	if err != nil {
		return rightSizeReport{}, err
	}
	observations := map[string]*rightSizeObservation{}
	for key, resourceValue := range resources {
		copyValue := resourceValue
		observations[key] = &rightSizeObservation{Resource: copyValue}
	}
	for _, sample := range samples {
		key := rightSizeMetricKey(sample.Namespace, sample.Pod, sample.Container)
		if observation, ok := observations[key]; ok {
			observation.CPUP95 = sample.CPU
			observation.MemoryP95 = sample.Memory
			observation.MemoryMax = sample.Memory
			observation.Observed = true
		}
	}
	return rightSizeReport{
		Source: "metrics.k8s.io", Window: "point-in-time", PointInTime: true,
		Containers: aggregateRightSizeObservations(observations, true),
	}, nil
}

type rightSizeTopSample struct {
	Namespace, Pod, Container string
	CPU, Memory               float64
}

func parseKubectlTopContainers(raw []byte, namespace string) ([]rightSizeTopSample, error) {
	var samples []rightSizeTopSample
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		expected := 5
		if namespace != "" {
			expected = 4
		}
		if len(fields) != expected {
			return nil, fmt.Errorf("parse kubectl top line %d: expected %d fields, got %d", lineNumber+1, expected, len(fields))
		}
		offset, sampleNamespace := 0, namespace
		if namespace == "" {
			sampleNamespace, offset = fields[0], 1
		}
		cpu, err := resource.ParseQuantity(fields[offset+2])
		if err != nil {
			if fields[offset+2] == "<unknown>" {
				continue
			}
			return nil, fmt.Errorf("parse CPU usage on line %d: %w", lineNumber+1, err)
		}
		memory, err := resource.ParseQuantity(fields[offset+3])
		if err != nil {
			if fields[offset+3] == "<unknown>" {
				continue
			}
			return nil, fmt.Errorf("parse memory usage on line %d: %w", lineNumber+1, err)
		}
		samples = append(samples, rightSizeTopSample{
			Namespace: sampleNamespace, Pod: fields[offset], Container: fields[offset+1],
			CPU: float64(cpu.MilliValue()) / 1000, Memory: float64(memory.Value()),
		})
	}
	return samples, nil
}

func aggregateRightSizeObservations(observations map[string]*rightSizeObservation, pointInTime bool) []rightSizeContainer {
	grouped := map[string]*rightSizeObservation{}
	for _, observation := range observations {
		if !observation.Observed {
			continue
		}
		workload := observation.Resource.Workload
		key := strings.Join([]string{workload.Namespace, workload.Kind, workload.Name, observation.Resource.Container}, "\x00")
		current, ok := grouped[key]
		if !ok {
			copyValue := *observation
			grouped[key] = &copyValue
			continue
		}
		current.CPUP95 = math.Max(current.CPUP95, observation.CPUP95)
		current.MemoryP95 = math.Max(current.MemoryP95, observation.MemoryP95)
		current.MemoryMax = math.Max(current.MemoryMax, observation.MemoryMax)
		current.Observed = true
		current.Resource.CPURequest = math.Max(current.Resource.CPURequest, observation.Resource.CPURequest)
		current.Resource.MemoryReq = math.Max(current.Resource.MemoryReq, observation.Resource.MemoryReq)
		current.Resource.MemoryLimit = math.Max(current.Resource.MemoryLimit, observation.Resource.MemoryLimit)
	}
	containers := make([]rightSizeContainer, 0, len(grouped))
	for _, observation := range grouped {
		containers = append(containers, classifyRightSizeContainer(*observation, pointInTime))
	}
	return containers
}

func classifyRightSizeContainer(observation rightSizeObservation, pointInTime bool) rightSizeContainer {
	resourceValue := observation.Resource
	container := rightSizeContainer{
		Namespace: resourceValue.Workload.Namespace, WorkloadKind: resourceValue.Workload.Kind,
		Workload: resourceValue.Workload.Name, Container: resourceValue.Container,
		CPURequestCores: resourceValue.CPURequest, CPUP95Cores: observation.CPUP95,
		MemoryRequestBytes: resourceValue.MemoryReq, MemoryP95Bytes: observation.MemoryP95,
		MemoryMaxBytes: observation.MemoryMax, MemoryLimitBytes: resourceValue.MemoryLimit,
		SuggestedCPUCores:    roundCPURequest(observation.CPUP95),
		SuggestedMemoryBytes: roundMemoryRequest(observation.MemoryP95), PointInTimeObservation: pointInTime,
	}
	container.CPURatio = safeRightSizeRatio(resourceValue.CPURequest, observation.CPUP95)
	container.MemoryRatio = safeRightSizeRatio(resourceValue.MemoryReq, observation.MemoryP95)
	container.Verdict, container.score = rightSizeVerdictFor(resourceValue, observation)
	container.EstimatedSavingsPct = estimatedRightSizeSavings(container)
	return container
}

func rightSizeVerdictFor(resources rightSizeResource, observation rightSizeObservation) (rightSizeVerdict, float64) {
	underCPU := observation.CPUP95 > resources.CPURequest
	underMemory := observation.MemoryP95 > resources.MemoryReq
	nearMemoryLimit := resources.MemoryLimit > 0 && observation.MemoryMax >= 0.9*resources.MemoryLimit
	if underCPU || underMemory || nearMemoryLimit {
		score := math.Max(usageToRequestRatio(observation.CPUP95, resources.CPURequest), usageToRequestRatio(observation.MemoryP95, resources.MemoryReq))
		if resources.MemoryLimit > 0 {
			score = math.Max(score, observation.MemoryMax/resources.MemoryLimit)
		}
		return rightSizeUnder, score
	}
	overCPU := resources.CPURequest > 2*observation.CPUP95 && resources.CPURequest-observation.CPUP95 > rightSizeCPUOverGap
	overMemory := resources.MemoryReq > 2*observation.MemoryP95 && resources.MemoryReq-observation.MemoryP95 > rightSizeMemoryOverGap
	if overCPU || overMemory {
		score := math.Max(requestToUsageRatio(resources.CPURequest, observation.CPUP95), requestToUsageRatio(resources.MemoryReq, observation.MemoryP95))
		return rightSizeOver, score
	}
	return rightSizeOK, 0
}

func finalizeRightSizeObservations(containers []rightSizeContainer, minSavings float64) []rightSizeContainer {
	filtered := containers[:0]
	for _, container := range containers {
		if minSavings > 0 {
			if container.Verdict == rightSizeOK || (container.Verdict == rightSizeOver && container.EstimatedSavingsPct < minSavings) {
				continue
			}
		}
		filtered = append(filtered, container)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left, right := rightSizeVerdictRank(filtered[i].Verdict), rightSizeVerdictRank(filtered[j].Verdict)
		if left != right {
			return left > right
		}
		if filtered[i].score != filtered[j].score {
			return filtered[i].score > filtered[j].score
		}
		return strings.Join([]string{filtered[i].Namespace, filtered[i].Workload, filtered[i].Container}, "/") <
			strings.Join([]string{filtered[j].Namespace, filtered[j].Workload, filtered[j].Container}, "/")
	})
	return filtered
}

func renderRightSizeReport(report rightSizeReport, output string) (string, error) {
	if output == "json" {
		raw, err := json.MarshalIndent(report, "", "  ")
		return string(raw), err
	}
	if output != "" && output != "table" {
		return "", fmt.Errorf("unsupported output format %q (table, json)", output)
	}
	var prefix strings.Builder
	if report.Warning != "" {
		prefix.WriteString(report.Warning)
		prefix.WriteString("\n")
	}
	fmt.Fprintf(&prefix, "Source: %s (%s)\n", report.Source, report.Window)
	rows := make([][]string, 0, len(report.Containers))
	for _, container := range report.Containers {
		rows = append(rows, []string{
			string(container.Verdict), container.Namespace, container.WorkloadKind + "/" + container.Workload, container.Container,
			formatCPU(container.CPURequestCores), formatCPU(container.CPUP95Cores), formatRatio(container.CPURatio),
			formatMemory(container.MemoryRequestBytes), formatMemory(container.MemoryP95Bytes), formatMemory(container.MemoryMaxBytes),
			formatRatio(container.MemoryRatio), formatCPU(container.SuggestedCPUCores), formatMemory(container.SuggestedMemoryBytes),
		})
	}
	cpuObserved, memoryObserved, memoryMax := "CPU P95", "MEM P95", "MEM MAX"
	if report.PointInTime {
		cpuObserved, memoryObserved, memoryMax = "CPU NOW", "MEM NOW", "MEM NOW"
	}
	return prefix.String() + ui.Table([]string{"VERDICT", "NAMESPACE", "WORKLOAD", "CONTAINER", "CPU REQ", cpuObserved, "REQ/USE", "MEM REQ", memoryObserved, memoryMax, "REQ/USE", "SUGGEST CPU", "SUGGEST MEM"}, rows), nil
}

func rightSizeResourcePromQL(metric, resourceName, namespace string) string {
	selector := fmt.Sprintf(`resource="%s",container!="",pod!=""`, resourceName)
	if namespace != "" {
		selector += `,namespace="` + namespace + `"`
	}
	return fmt.Sprintf(`max by (namespace,pod,container) (%s{%s})`, metric, selector)
}

func prometheusDuration(duration time.Duration) string {
	if duration%time.Hour == 0 {
		return strconv.FormatInt(int64(duration/time.Hour), 10) + "h"
	}
	if duration%time.Minute == 0 {
		return strconv.FormatInt(int64(duration/time.Minute), 10) + "m"
	}
	return strconv.FormatInt(int64(duration/time.Second), 10) + "s"
}

func parseRightSizeWindow(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid --window %q", value)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --window %q: %w", value, err)
	}
	return duration, nil
}

func rightSizeMetricKey(namespace, pod, container string) string {
	return strings.Join([]string{namespace, pod, container}, "\x00")
}

func rightSizePodKey(namespace, pod string) string { return namespace + "\x00" + pod }

func parseRightSizeQuantity(value string, cpu bool) float64 {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return 0
	}
	if cpu {
		return float64(quantity.MilliValue()) / 1000
	}
	return float64(quantity.Value())
}

func safeRightSizeRatio(request, usage float64) *float64 {
	if request <= 0 || usage <= 0 {
		return nil
	}
	ratio := request / usage
	return &ratio
}

func usageToRequestRatio(usage, request float64) float64 {
	if usage <= 0 {
		return 0
	}
	if request <= 0 {
		return math.Inf(1)
	}
	return usage / request
}

func requestToUsageRatio(request, usage float64) float64 {
	if request <= 0 {
		return 0
	}
	if usage <= 0 {
		return math.Inf(1)
	}
	return request / usage
}

func roundCPURequest(cores float64) float64 {
	if cores <= 0 {
		return 0
	}
	return math.Ceil(cores/0.01-1e-9) * 0.01
}

func roundMemoryRequest(bytes float64) float64 {
	if bytes <= 0 {
		return 0
	}
	const step = 16 * 1024 * 1024
	return math.Ceil(bytes/step-1e-9) * step
}

func estimatedRightSizeSavings(container rightSizeContainer) float64 {
	var savings float64
	if container.CPURequestCores > 0 && container.SuggestedCPUCores < container.CPURequestCores {
		savings = (container.CPURequestCores - container.SuggestedCPUCores) / container.CPURequestCores * 100
	}
	if container.MemoryRequestBytes > 0 && container.SuggestedMemoryBytes < container.MemoryRequestBytes {
		savings = math.Max(savings, (container.MemoryRequestBytes-container.SuggestedMemoryBytes)/container.MemoryRequestBytes*100)
	}
	return math.Round(savings*10) / 10
}

func rightSizeVerdictRank(verdict rightSizeVerdict) int {
	switch verdict {
	case rightSizeUnder:
		return 2
	case rightSizeOver:
		return 1
	default:
		return 0
	}
}

func formatCPU(cores float64) string {
	if cores <= 0 {
		return "-"
	}
	return strconv.FormatInt(int64(math.Round(cores*1000)), 10) + "m"
}

func formatMemory(bytes float64) string {
	if bytes <= 0 {
		return "-"
	}
	// resource.Quantity prints raw decimal bytes when the value is not
	// 1024-aligned; always humanize instead (Mi above 10Mi, Ki below).
	const (
		ki = 1024
		mi = 1024 * 1024
	)
	if bytes >= 10*mi {
		return strconv.FormatInt(int64(math.Round(bytes/mi)), 10) + "Mi"
	}
	return strconv.FormatInt(int64(math.Round(bytes/ki)), 10) + "Ki"
}

func formatRatio(ratio *float64) string {
	if ratio == nil {
		return "-"
	}
	return fmt.Sprintf("%.2fx", *ratio)
}
