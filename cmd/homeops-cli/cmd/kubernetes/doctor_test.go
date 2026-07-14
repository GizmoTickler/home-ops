package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeKubectlDoctor(t *testing.T, byKind map[string]string) func(args ...string) ([]byte, error) {
	t.Helper()
	return func(args ...string) ([]byte, error) {
		kind := ""
		for i, a := range args {
			if a == "get" && i+1 < len(args) {
				kind = args[i+1]
				break
			}
		}
		if body, ok := byKind[kind]; ok {
			return []byte(body), nil
		}
		return []byte(`{"items":[]}`), nil
	}
}

func statusFor(t *testing.T, r doctorReport, group string) []doctorCheck {
	t.Helper()
	var out []doctorCheck
	for _, c := range r.Checks {
		if c.Group == group {
			out = append(out, c)
		}
	}
	return out
}

func TestBuildDoctorReportFlux(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })

	ks := `{"items":[
	  {"metadata":{"name":"good","namespace":"flux-system"},"spec":{"suspend":false},"status":{"conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded"}]}},
	  {"metadata":{"name":"broken","namespace":"media"},"spec":{"suspend":false},"status":{"conditions":[{"type":"Ready","status":"False","reason":"BuildFailed","message":"kustomize build failed"}]}},
	  {"metadata":{"name":"paused","namespace":"media"},"spec":{"suspend":true},"status":{"conditions":[{"type":"Ready","status":"True","reason":"ok"}]}}
	]}`
	hr := `{"items":[
	  {"metadata":{"name":"app","namespace":"media"},"spec":{"suspend":false},"status":{"conditions":[{"type":"Ready","status":"False","reason":"InstallFailed","message":"boom"}]}}
	]}`
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{
		fluxKustomizationResource: ks,
		fluxHelmReleaseResource:   hr,
	})

	r := buildDoctorReport("", doctorDefaultPendingGrace)
	flux := statusFor(t, r, doctorGroupFlux)

	var fails, warns int
	for _, c := range flux {
		switch c.Status {
		case statusFail:
			fails++
		case statusWarn:
			warns++
		}
	}
	assert.Equal(t, 2, fails, "expected two FAIL rows (broken KS, failed HR)")
	assert.Equal(t, 1, warns, "expected one WARN row (suspended KS)")
	assert.True(t, r.hasFail())
}

func TestBuildDoctorReportFluxAllHealthy(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{
		fluxKustomizationResource: `{"items":[{"metadata":{"name":"ok","namespace":"flux-system"},"spec":{"suspend":false},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`,
	})
	r := buildDoctorReport("", doctorDefaultPendingGrace)
	flux := statusFor(t, r, doctorGroupFlux)
	require.Len(t, flux, 1)
	assert.Equal(t, statusPass, flux[0].Status)
	assert.False(t, r.hasFail())
}

func TestBuildDoctorReportNodesReadyPasses(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{
		"nodes": `{"items":[{"metadata":{"name":"k8s-0"},"status":{"conditions":[
			{"type":"Ready","status":"True"},
			{"type":"MemoryPressure","status":"False"},
			{"type":"DiskPressure","status":"False"},
			{"type":"PIDPressure","status":"False"}
		]}}]}`,
	})

	r := buildDoctorReport("", doctorDefaultPendingGrace)
	nodes := statusFor(t, r, doctorGroupNodes)
	require.Len(t, nodes, 1)
	assert.Equal(t, statusPass, nodes[0].Status)
	assert.False(t, r.hasFail())
}

func TestBuildDoctorReportNodesNotReadyFails(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{
		"nodes": `{"items":[{"metadata":{"name":"k8s-1"},"status":{"conditions":[
			{"type":"Ready","status":"False","reason":"KubeletNotReady","message":"container runtime down"}
		]}}]}`,
	})

	r := buildDoctorReport("", doctorDefaultPendingGrace)
	nodes := statusFor(t, r, doctorGroupNodes)
	require.Len(t, nodes, 1)
	assert.Equal(t, statusFail, nodes[0].Status)
	assert.Equal(t, "k8s-1", nodes[0].Name)
	assert.Contains(t, nodes[0].Detail, "KubeletNotReady")
	assert.True(t, r.hasFail())
}

func TestBuildDoctorReportNodesPressureWarns(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{
		"nodes": `{"items":[{"metadata":{"name":"k8s-2"},"status":{"conditions":[
			{"type":"Ready","status":"True"},
			{"type":"MemoryPressure","status":"True"},
			{"type":"DiskPressure","status":"False"},
			{"type":"PIDPressure","status":"False"}
		]}}]}`,
	})

	r := buildDoctorReport("", doctorDefaultPendingGrace)
	nodes := statusFor(t, r, doctorGroupNodes)
	require.Len(t, nodes, 1)
	assert.Equal(t, statusWarn, nodes[0].Status)
	assert.Equal(t, "MemoryPressure=True", nodes[0].Detail)
	assert.False(t, r.hasFail())
}

func TestBuildDoctorReportNodesCordonedWarns(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{
		"nodes": `{"items":[{"metadata":{"name":"k8s-3"},"spec":{"unschedulable":true},"status":{"conditions":[
			{"type":"Ready","status":"True"},
			{"type":"MemoryPressure","status":"False"},
			{"type":"DiskPressure","status":"False"},
			{"type":"PIDPressure","status":"False"}
		]}}]}`,
	})

	r := buildDoctorReport("", doctorDefaultPendingGrace)
	nodes := statusFor(t, r, doctorGroupNodes)
	require.Len(t, nodes, 1)
	assert.Equal(t, statusWarn, nodes[0].Status)
	assert.Equal(t, "unschedulable", nodes[0].Detail)
	assert.False(t, r.hasFail())
}

func TestBuildDoctorReportPods(t *testing.T) {
	old := kubectlOutputFn
	oldNow := nowFn
	t.Cleanup(func() { kubectlOutputFn = old; nowFn = oldNow })
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }

	recent := now.Add(-10 * time.Minute).Format(time.RFC3339)
	stale := now.Add(-72 * time.Hour).Format(time.RFC3339)
	pods := `{"items":[
	  {"metadata":{"name":"crash","namespace":"media"},"status":{"phase":"Running","containerStatuses":[{"name":"app","state":{"waiting":{"reason":"CrashLoopBackOff","message":"back-off"}}}]}},
	  {"metadata":{"name":"imgpull","namespace":"media"},"status":{"phase":"Pending","containerStatuses":[{"name":"app","state":{"waiting":{"reason":"ImagePullBackOff"}}}]}},
	  {"metadata":{"name":"pending","namespace":"media"},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}},
	  {"metadata":{"name":"oomrecent","namespace":"media"},"status":{"phase":"Running","containerStatuses":[{"name":"app","lastState":{"terminated":{"reason":"OOMKilled","finishedAt":"` + recent + `"}},"state":{"running":{}}}]}},
	  {"metadata":{"name":"oomold","namespace":"media"},"status":{"phase":"Running","containerStatuses":[{"name":"app","lastState":{"terminated":{"reason":"OOMKilled","finishedAt":"` + stale + `"}},"state":{"running":{}}}]}},
	  {"metadata":{"name":"healthy","namespace":"media"},"status":{"phase":"Running","containerStatuses":[{"name":"app","state":{"running":{}}}]}}
	]}`
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{"pods": pods})

	r := buildDoctorReport("", doctorDefaultPendingGrace)
	names := map[string]doctorStatus{}
	for _, c := range statusFor(t, r, doctorGroupPods) {
		names[c.Name] = c.Status
	}
	assert.Equal(t, statusFail, names["media/crash"])
	assert.Equal(t, statusFail, names["media/imgpull"])
	assert.Equal(t, statusFail, names["media/pending"])
	assert.Equal(t, statusWarn, names["media/oomrecent"])
	_, hasOld := names["media/oomold"]
	assert.False(t, hasOld, "stale OOM should not be reported")
	_, hasHealthy := names["media/healthy"]
	assert.False(t, hasHealthy)
}

func TestDoctorIgnoresPendingPodsYoungerThanGrace(t *testing.T) {
	old := kubectlOutputFn
	oldNow := nowFn
	t.Cleanup(func() { kubectlOutputFn = old; nowFn = oldNow })
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }

	created := now.Add(-2 * time.Minute).Format(time.RFC3339)
	pods := `{"items":[
	  {"metadata":{"name":"volsync-src-radarr","namespace":"downloads","creationTimestamp":"` + created + `"},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}}
	]}`
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{"pods": pods})

	r := buildDoctorReport("", 10*time.Minute)
	for _, c := range statusFor(t, r, doctorGroupPods) {
		assert.NotEqual(t, "downloads/volsync-src-radarr", c.Name, "young Pending pod should be ignored entirely")
	}
	assert.False(t, r.hasFail())
}

func TestDoctorFailsPendingPodsOlderThanGrace(t *testing.T) {
	old := kubectlOutputFn
	oldNow := nowFn
	t.Cleanup(func() { kubectlOutputFn = old; nowFn = oldNow })
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }

	created := now.Add(-11 * time.Minute).Format(time.RFC3339)
	pods := `{"items":[
	  {"metadata":{"name":"stuck","namespace":"downloads","creationTimestamp":"` + created + `"},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}}
	]}`
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{"pods": pods})

	r := buildDoctorReport("", 10*time.Minute)
	names := map[string]doctorStatus{}
	for _, c := range statusFor(t, r, doctorGroupPods) {
		names[c.Name] = c.Status
	}
	assert.Equal(t, statusFail, names["downloads/stuck"])
	assert.True(t, r.hasFail())
}

func TestDoctorPendingGraceFlagOverride(t *testing.T) {
	old := kubectlOutputFn
	oldNow := nowFn
	t.Cleanup(func() { kubectlOutputFn = old; nowFn = oldNow })
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }

	created := now.Add(-15 * time.Minute).Format(time.RFC3339)
	pods := `{"items":[
	  {"metadata":{"name":"volsync-src-sonarr","namespace":"downloads","creationTimestamp":"` + created + `"},"status":{"phase":"Pending","conditions":[{"type":"PodScheduled","status":"False","reason":"Unschedulable"}]}}
	]}`
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{"pods": pods})

	cmd := newDoctorCommand()
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--output", "json", "--pending-grace", "20m"})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "volsync-src-sonarr")
}

func TestBuildDoctorReportCeph(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })

	cases := map[string]doctorStatus{
		"HEALTH_OK":   statusPass,
		"HEALTH_WARN": statusWarn,
		"HEALTH_ERR":  statusFail,
	}
	for health, want := range cases {
		ceph := `{"items":[{"metadata":{"name":"rook-ceph","namespace":"rook-ceph"},"status":{"ceph":{"health":"` + health + `"}}}]}`
		kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{cephClusterResource: ceph})
		r := buildDoctorReport("", doctorDefaultPendingGrace)
		ck := statusFor(t, r, doctorGroupCeph)
		require.Len(t, ck, 1, "health=%s", health)
		assert.Equal(t, want, ck[0].Status, "health=%s", health)
	}
}

func TestBuildDoctorReportCerts(t *testing.T) {
	old := kubectlOutputFn
	oldNow := nowFn
	t.Cleanup(func() { kubectlOutputFn = old; nowFn = oldNow })
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }

	soon := now.Add(5 * 24 * time.Hour).Format(time.RFC3339)
	later := now.Add(60 * 24 * time.Hour).Format(time.RFC3339)
	certs := `{"items":[
	  {"metadata":{"name":"ok","namespace":"network"},"status":{"notAfter":"` + later + `","conditions":[{"type":"Ready","status":"True"}]}},
	  {"metadata":{"name":"expiring","namespace":"network"},"status":{"notAfter":"` + soon + `","conditions":[{"type":"Ready","status":"True"}]}},
	  {"metadata":{"name":"notready","namespace":"network"},"status":{"notAfter":"` + later + `","conditions":[{"type":"Ready","status":"False","reason":"Issuing","message":"pending"}]}}
	]}`
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{certificateResource: certs})

	r := buildDoctorReport("", doctorDefaultPendingGrace)
	names := map[string]doctorStatus{}
	for _, c := range statusFor(t, r, doctorGroupCerts) {
		names[c.Name] = c.Status
	}
	assert.Equal(t, statusFail, names["network/notready"])
	assert.Equal(t, statusWarn, names["network/expiring"])
	_, hasOK := names["network/ok"]
	assert.False(t, hasOK, "healthy cert should not get its own row")
}

func TestDoctorNamespaceScoping(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	var sawArgs [][]string
	kubectlOutputFn = func(args ...string) ([]byte, error) {
		sawArgs = append(sawArgs, append([]string(nil), args...))
		return []byte(`{"items":[]}`), nil
	}
	_ = buildDoctorReport("media", doctorDefaultPendingGrace)
	for _, a := range sawArgs {
		joined := strings.Join(a, " ")
		if strings.Contains(joined, "get nodes ") {
			assert.NotContains(t, joined, "--namespace")
			assert.NotContains(t, joined, "-A")
			continue
		}
		assert.Contains(t, joined, "--namespace media")
		assert.NotContains(t, joined, "-A")
	}
}

func TestRenderDoctorReportJSON(t *testing.T) {
	r := doctorReport{
		Checks: []doctorCheck{
			{Group: doctorGroupCeph, Name: "rook-ceph/rook-ceph", Status: statusFail, Detail: "HEALTH_ERR"},
		},
	}
	r.finalize()
	out, err := renderDoctorReport(r, "json")
	require.NoError(t, err)

	var decoded doctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, 1, decoded.Summary.Fail)
	require.Len(t, decoded.Checks, 1)
	assert.Equal(t, statusFail, decoded.Checks[0].Status)
}

func TestRunDoctorExitsNonZeroOnFail(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{
		cephClusterResource: `{"items":[{"metadata":{"name":"rook-ceph","namespace":"rook-ceph"},"status":{"ceph":{"health":"HEALTH_ERR"}}}]}`,
	})
	var buf strings.Builder
	err := runDoctor("", "json", doctorDefaultPendingGrace, &buf)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "HEALTH_ERR")
}

func TestRunDoctorAllPass(t *testing.T) {
	old := kubectlOutputFn
	t.Cleanup(func() { kubectlOutputFn = old })
	kubectlOutputFn = fakeKubectlDoctor(t, map[string]string{})
	var buf strings.Builder
	err := runDoctor("", "table", doctorDefaultPendingGrace, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "PASS")
}
