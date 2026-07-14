package opvault

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useAuditFakes swaps both the legacy and context-aware audit seams to the
// same fakes so ordered-behavior tests remain hermetic regardless of which
// path production code takes.
func useAuditFakes(t *testing.T, kubectl func(args ...string) ([]byte, error), op func(args ...string) ([]byte, error)) {
	t.Helper()
	oldKubectl, oldKubectlCtx := opAuditKubectlOutputFn, opAuditKubectlOutputCtxFn
	oldOp, oldOpCtx := runOpFn, runOpCtxFn
	opAuditKubectlOutputFn = kubectl
	opAuditKubectlOutputCtxFn = func(ctx context.Context, args ...string) ([]byte, error) { return kubectl(args...) }
	runOpFn = op
	runOpCtxFn = func(ctx context.Context, args ...string) ([]byte, error) { return op(args...) }
	t.Cleanup(func() {
		opAuditKubectlOutputFn, opAuditKubectlOutputCtxFn = oldKubectl, oldKubectlCtx
		runOpFn, runOpCtxFn = oldOp, oldOpCtx
	})
}

func TestBuildAuditReportFindsExternalSecretReadinessAndItemGaps(t *testing.T) {
	useAuditFakes(t, func(args ...string) ([]byte, error) {
		return []byte(`{"items":[
		  {"metadata":{"name":"cluster-config","namespace":"flux-system"},"spec":{"dataFrom":[{"extract":{"key":"cluster-config"}},{"extract":{"key":"missing-item"}}]},"status":{"conditions":[{"type":"Ready","status":"True","reason":"SecretSynced"}]}},
		  {"metadata":{"name":"broken","namespace":"media"},"spec":{"data":[{"secretKey":"password","remoteRef":{"key":"radarr"}}]},"status":{"conditions":[{"type":"Ready","status":"False","reason":"ProviderError","message":"item not found"}]}}
		]}`), nil
	}, func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "vault list --format=json":
			return []byte(`[{"id":"v1","name":"HomeOps"}]`), nil
		case "item list --vault HomeOps --format=json":
			return []byte(`[{"id":"i1","title":"cluster-config"},{"id":"i2","title":"radarr"},{"id":"i3","title":"orphan"}]`), nil
		default:
			t.Fatalf("unexpected op args: %v", args)
			return nil, nil
		}
	})

	r := buildAuditReport("all")
	assert.True(t, r.hasFail())
	require.Len(t, r.ExternalSecrets, 2)
	assert.Equal(t, auditPass, r.ExternalSecrets[0].Status)
	assert.Equal(t, auditFail, r.ExternalSecrets[1].Status)
	require.Len(t, r.MissingItems, 1)
	assert.Equal(t, "missing-item", r.MissingItems[0].Item)
	require.Len(t, r.OrphanItems, 1)
	assert.Equal(t, "orphan", r.OrphanItems[0].Item)
	assert.Equal(t, auditWarn, r.OrphanItems[0].Status)
}

func TestAuditReportReferencesRemoteRefKeys(t *testing.T) {
	useAuditFakes(t, func(args ...string) ([]byte, error) {
		return []byte(`{"items":[
		  {"metadata":{"name":"app","namespace":"media"},"spec":{"data":[{"secretKey":"token","remoteRef":{"key":"cloudflare","property":"token"}}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}
		]}`), nil
	}, func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "vault list --format=json" {
			return []byte(`[{"name":"HomeOps"}]`), nil
		}
		return []byte(`[{"title":"cloudflare"}]`), nil
	})

	r := buildAuditReport("all")
	require.Empty(t, r.MissingItems)
	require.Len(t, r.References, 1)
	assert.Equal(t, "cloudflare", r.References[0].Item)
	assert.Equal(t, "media/app", r.References[0].ExternalSecret)
}

func TestAuditVaultScope(t *testing.T) {
	var opCalls []string
	useAuditFakes(t, func(args ...string) ([]byte, error) {
		return []byte(`{"items":[]}`), nil
	}, func(args ...string) ([]byte, error) {
		opCalls = append(opCalls, strings.Join(args, " "))
		return []byte(`[]`), nil
	})

	_ = buildAuditReport("HomeOps")
	assert.Equal(t, []string{"item list --vault HomeOps --format=json"}, opCalls)
}

func TestRenderAuditJSON(t *testing.T) {
	r := auditReport{
		Summary: auditSummary{Pass: 1, Warn: 1, Fail: 1},
		MissingItems: []auditItemFinding{
			{Item: "missing", Status: auditFail},
		},
	}
	out, err := renderAuditReport(r, "json")
	require.NoError(t, err)
	var decoded auditReport
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, 1, decoded.Summary.Fail)
	require.Len(t, decoded.MissingItems, 1)
}

func TestRunAuditFailsForMissingItem(t *testing.T) {
	useAuditFakes(t, func(args ...string) ([]byte, error) {
		return []byte(`{"items":[{"metadata":{"name":"app","namespace":"media"},"spec":{"dataFrom":[{"extract":{"key":"missing"}}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`), nil
	}, func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "vault list --format=json" {
			return []byte(`[{"name":"HomeOps"}]`), nil
		}
		return []byte(`[]`), nil
	})
	var buf strings.Builder
	err := runAudit("all", "json", &buf)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "missing")
}

func TestRunAuditContextThreadsCallerContext(t *testing.T) {
	oldKubectlCtx := opAuditKubectlOutputCtxFn
	oldOpCtx := runOpCtxFn
	t.Cleanup(func() { opAuditKubectlOutputCtxFn = oldKubectlCtx; runOpCtxFn = oldOpCtx })

	type auditContextKey struct{}
	key := auditContextKey{}
	ctx := context.WithValue(context.Background(), key, "op-audit")
	var sawKubectlContext, sawOpContext bool
	opAuditKubectlOutputCtxFn = func(ctx context.Context, args ...string) ([]byte, error) {
		sawKubectlContext = sawKubectlContext || ctx.Value(key) == "op-audit"
		return []byte(`{"items":[]}`), nil
	}
	runOpCtxFn = func(ctx context.Context, args ...string) ([]byte, error) {
		sawOpContext = sawOpContext || ctx.Value(key) == "op-audit"
		return []byte(`[]`), nil
	}

	var buf strings.Builder
	err := runAuditContext(ctx, "all", "json", &buf)

	require.NoError(t, err)
	assert.True(t, sawKubectlContext)
	assert.True(t, sawOpContext)
}
