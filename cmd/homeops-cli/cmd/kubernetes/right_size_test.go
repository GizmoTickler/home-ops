package kubernetes

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/config"
	"homeops-cli/internal/testutil"
)

func TestParseRightSizePromQLResponse(t *testing.T) {
	raw := []byte(`{
  "status":"success",
  "data":{"resultType":"vector","result":[
    {"metric":{"namespace":"media","pod":"radarr-1","container":"app"},"value":[1234.5,"0.125"]},
    {"metric":{"namespace":"media","pod":"radarr-2","container":"app"},"value":[1234.5,"268435456"]}
  ]}
}`)
	samples, err := parseRightSizePromQLResponse(raw)
	require.NoError(t, err)
	require.Len(t, samples, 2)
	assert.Equal(t, rightSizeMetricSample{Namespace: "media", Pod: "radarr-1", Container: "app", Value: 0.125}, samples[0])
	assert.Equal(t, float64(256*1024*1024), samples[1].Value)

	_, err = parseRightSizePromQLResponse([]byte(`{"status":"error","error":"bad query"}`))
	assert.ErrorContains(t, err, "bad query")
	_, err = parseRightSizePromQLResponse([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	assert.ErrorContains(t, err, "result type")
	_, err = parseRightSizePromQLResponse([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"NaN"]}]}}`))
	assert.ErrorContains(t, err, "invalid sample")
}

func TestRightSizeVerdictMathEdgeCases(t *testing.T) {
	const mib = float64(1024 * 1024)
	tests := []struct {
		name        string
		resources   rightSizeResource
		observation rightSizeObservation
		want        rightSizeVerdict
	}{
		{
			name: "over both thresholds", resources: rightSizeResource{CPURequest: 0.5, MemoryReq: 512 * mib},
			observation: rightSizeObservation{CPUP95: 0.1, MemoryP95: 128 * mib, Observed: true}, want: rightSizeOver,
		},
		{
			name: "ratio alone is noise", resources: rightSizeResource{CPURequest: 0.1, MemoryReq: 128 * mib},
			observation: rightSizeObservation{CPUP95: 0.01, MemoryP95: 1}, want: rightSizeOK,
		},
		{
			name: "cpu p95 above request", resources: rightSizeResource{CPURequest: 0.1, MemoryReq: 256 * mib},
			observation: rightSizeObservation{CPUP95: 0.11, MemoryP95: 128 * mib}, want: rightSizeUnder,
		},
		{
			name: "no request with usage", resources: rightSizeResource{},
			observation: rightSizeObservation{CPUP95: 0.01}, want: rightSizeUnder,
		},
		{
			name: "no request and zero usage", resources: rightSizeResource{},
			observation: rightSizeObservation{}, want: rightSizeOK,
		},
		{
			name: "memory max near limit", resources: rightSizeResource{CPURequest: 0.1, MemoryReq: 256 * mib, MemoryLimit: 512 * mib},
			observation: rightSizeObservation{CPUP95: 0.1, MemoryP95: 256 * mib, MemoryMax: 0.9 * 512 * mib}, want: rightSizeUnder,
		},
		{
			name: "no limit does not trigger max rule", resources: rightSizeResource{CPURequest: 0.1, MemoryReq: 256 * mib},
			observation: rightSizeObservation{CPUP95: 0.1, MemoryP95: 256 * mib, MemoryMax: 1024 * mib}, want: rightSizeOK,
		},
		{
			name: "under wins over over", resources: rightSizeResource{CPURequest: 0.1, MemoryReq: 1024 * mib},
			observation: rightSizeObservation{CPUP95: 0.2, MemoryP95: 64 * mib}, want: rightSizeUnder,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verdict, _ := rightSizeVerdictFor(tt.resources, tt.observation)
			assert.Equal(t, tt.want, verdict)
		})
	}

	assert.Nil(t, safeRightSizeRatio(1, 0))
	ratio := safeRightSizeRatio(0.5, 0.1)
	require.NotNil(t, ratio)
	assert.InDelta(t, 5, *ratio, 0.0001)
}

func TestRightSizeSuggestionRounding(t *testing.T) {
	assert.Zero(t, roundCPURequest(0))
	assert.InDelta(t, 0.01, roundCPURequest(0.001), 0.000001)
	assert.InDelta(t, 0.13, roundCPURequest(0.121), 0.000001)
	assert.InDelta(t, 0.5, roundCPURequest(0.5), 0.000001)

	const mib = float64(1024 * 1024)
	assert.Zero(t, roundMemoryRequest(0))
	assert.Equal(t, 16*mib, roundMemoryRequest(1))
	assert.Equal(t, 144*mib, roundMemoryRequest(129*mib))
	assert.Equal(t, 256*mib, roundMemoryRequest(256*mib))
}

func TestRightSizeFallbackIsClearlyLabeled(t *testing.T) {
	cfg := &config.Config{Cluster: config.ClusterConfig{Observability: config.ObservabilityConfig{Namespace: "metrics"}}}
	testutil.Swap(t, &rightSizeConfigFn, func() *config.Config { return cfg })
	testutil.Swap(t, &rightSizeKubectlOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "get pods -A -o json":
			return []byte(`{"items":[{"metadata":{"name":"app-abc","namespace":"media","ownerReferences":[{"kind":"ReplicaSet","name":"app-123"}]},"spec":{"containers":[{"name":"app","resources":{"requests":{"cpu":"500m","memory":"512Mi"},"limits":{"memory":"1Gi"}}}]}}]}`), nil
		case "get replicasets.apps -A -o json":
			return []byte(`{"items":[{"metadata":{"name":"app-123","namespace":"media","ownerReferences":[{"kind":"Deployment","name":"app"}]}}]}`), nil
		case "get services --namespace metrics -o json":
			return []byte(`{"items":[]}`), nil
		case "top pods --containers --no-headers -A":
			return []byte("media app-abc app 50m 100Mi\n"), nil
		default:
			return nil, errors.New("unexpected kubectl call: " + strings.Join(args, " "))
		}
	})

	report, err := buildRightSizeReport(context.Background(), "", rightSizeDefaultWindow, 0)
	require.NoError(t, err)
	assert.Equal(t, "metrics.k8s.io", report.Source)
	assert.True(t, report.PointInTime)
	assert.Contains(t, report.Warning, "WARN")
	assert.Contains(t, report.Warning, "point-in-time")
	require.Len(t, report.Containers, 1)
	assert.True(t, report.Containers[0].PointInTimeObservation)
	assert.Equal(t, "Deployment", report.Containers[0].WorkloadKind)
	assert.Equal(t, rightSizeOver, report.Containers[0].Verdict)

	table, err := renderRightSizeReport(report, "table")
	require.NoError(t, err)
	assert.Contains(t, table, "WARN: VictoriaMetrics unavailable")
	assert.Contains(t, table, "CPU NOW")
	jsonOutput, err := renderRightSizeReport(report, "json")
	require.NoError(t, err)
	assert.Contains(t, jsonOutput, `"point_in_time": true`)
}

func TestDiscoverVictoriaMetricsServiceByNameAndPort(t *testing.T) {
	cfg := &config.Config{Cluster: config.ClusterConfig{Observability: config.ObservabilityConfig{Namespace: "observe"}}}
	testutil.Swap(t, &rightSizeConfigFn, func() *config.Config { return cfg })
	testutil.Swap(t, &rightSizeKubectlOutputFn, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"items":[
          {"metadata":{"name":"grafana"},"spec":{"ports":[{"name":"http","port":80}]}},
          {"metadata":{"name":"vmselect-cluster"},"spec":{"ports":[{"name":"metrics","port":8429}]}},
          {"metadata":{"name":"vmsingle-main"},"spec":{"ports":[{"name":"http","port":8429}]}}
        ]}`), nil
	})
	name, port, err := discoverVictoriaMetricsService(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "vmselect-cluster", name)
	assert.Equal(t, 8429, port)
}

func TestBuildVictoriaMetricsRightSizeReportJoinsUsageAndResources(t *testing.T) {
	cfg := &config.Config{Cluster: config.ClusterConfig{Observability: config.ObservabilityConfig{Namespace: "observe"}}}
	testutil.Swap(t, &rightSizeConfigFn, func() *config.Config { return cfg })
	testutil.Swap(t, &rightSizeKubectlOutputFn, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"items":[{"metadata":{"name":"vmselect-main"},"spec":{"ports":[{"name":"http","port":8429}]}}]}`), nil
	})
	testutil.Swap(t, &rightSizePortForwardFn, func(_ context.Context, namespace, service string, port int) (string, func() error, error) {
		assert.Equal(t, "observe", namespace)
		assert.Equal(t, "vmselect-main", service)
		assert.Equal(t, 8429, port)
		return "http://127.0.0.1:12345", func() error { return nil }, nil
	})
	const mib = float64(1024 * 1024)
	testutil.Swap(t, &rightSizePromQueryFn, func(_ context.Context, baseURL, query string) ([]byte, error) {
		assert.Equal(t, "http://127.0.0.1:12345/select/0/prometheus", baseURL)
		value := 0.0
		switch {
		case strings.Contains(query, "container_cpu_usage_seconds_total"):
			value = 0.1
		case strings.Contains(query, "quantile_over_time") && strings.Contains(query, "memory"):
			value = 100 * mib
		case strings.Contains(query, "max_over_time"):
			value = 200 * mib
		case strings.Contains(query, "resource_requests") && strings.Contains(query, `resource="cpu"`):
			value = 0.5
		case strings.Contains(query, "resource_requests") && strings.Contains(query, `resource="memory"`):
			value = 512 * mib
		case strings.Contains(query, "resource_limits"):
			value = 1024 * mib
		default:
			return nil, errors.New("unexpected query: " + query)
		}
		return rightSizePromVector("media", "app-1", "app", value), nil
	})
	workload := rightSizeWorkloadRef{Namespace: "media", Kind: "Deployment", Name: "app"}
	key := rightSizeMetricKey("media", "app-1", "app")
	report, err := buildVictoriaMetricsRightSizeReport(context.Background(), "media", 7*24*time.Hour,
		map[string]rightSizeResource{key: {Workload: workload, Container: "app"}},
		map[string]rightSizeWorkloadRef{rightSizePodKey("media", "app-1"): workload})
	require.NoError(t, err)
	require.Len(t, report.Containers, 1)
	container := report.Containers[0]
	assert.Equal(t, "victoriametrics", report.Source)
	assert.InDelta(t, 0.5, container.CPURequestCores, 0.0001)
	assert.InDelta(t, 0.1, container.CPUP95Cores, 0.0001)
	assert.Equal(t, 512*mib, container.MemoryRequestBytes)
	assert.Equal(t, 100*mib, container.MemoryP95Bytes)
	assert.Equal(t, 200*mib, container.MemoryMaxBytes)
	assert.Equal(t, rightSizeOver, container.Verdict)
}

func rightSizePromVector(namespace, pod, container string, value float64) []byte {
	return []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"namespace":"` + namespace +
		`","pod":"` + pod + `","container":"` + container + `"},"value":[1,"` +
		strconv.FormatFloat(value, 'f', -1, 64) + `"]}]}}`)
}

func TestParseRightSizeWindowSupportsDays(t *testing.T) {
	duration, err := parseRightSizeWindow("7d")
	require.NoError(t, err)
	assert.Equal(t, 168*time.Hour, duration)
	_, err = parseRightSizeWindow("wat")
	assert.Error(t, err)
}

func TestFinalizeRightSizeObservationsFiltersSavingsAndSorts(t *testing.T) {
	containers := []rightSizeContainer{
		{Workload: "ok", Verdict: rightSizeOK},
		{Workload: "small", Verdict: rightSizeOver, EstimatedSavingsPct: 10, score: 5},
		{Workload: "large", Verdict: rightSizeOver, EstimatedSavingsPct: 80, score: 4},
		{Workload: "danger", Verdict: rightSizeUnder, score: 1},
	}
	got := finalizeRightSizeObservations(containers, 25)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"danger", "large"}, []string{got[0].Workload, got[1].Workload})
}
