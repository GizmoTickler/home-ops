package volsync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"homeops-cli/internal/testutil"
)

func TestVerifyAllIterationFiltersLimitAndCheckPassthrough(t *testing.T) {
	testutil.Swap(t, &verifyAllListFn, func(context.Context, string) ([]ReplicationSource, error) {
		return []ReplicationSource{
			{Namespace: "downloads", Name: "radarr"},
			{Namespace: "media", Name: "plex"},
			{Namespace: "self-hosted", Name: "paperless"},
		}, nil
	})
	var confirmation string
	testutil.Swap(t, &confirmActionFn, func(message string, _ bool) (bool, error) {
		confirmation = message
		return true, nil
	})
	var calls []verifyOptions
	testutil.Swap(t, &verifyAllRunOneFn, func(_ context.Context, options verifyOptions) (verifyReport, error) {
		calls = append(calls, options)
		return verifyReport{IntegrityChecked: options.Check}, nil
	})

	var output bytes.Buffer
	err := runVolsyncVerifyAll(context.Background(), verifyAllOptions{
		Skip: "plex", Limit: 1, Timeout: time.Minute, MaxDuration: time.Hour, Check: true, Output: "table",
	}, &output)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, "radarr", calls[0].App)
	assert.Equal(t, "downloads", calls[0].Namespace)
	assert.True(t, calls[0].Check)
	assert.Contains(t, confirmation, "1 VolSync application")
	assert.Contains(t, confirmation, "downloads/radarr")
	assert.NotContains(t, confirmation, "plex")
	assert.Contains(t, output.String(), "integrity check passed")
}

func TestVerifyAllBudgetExpirySkipsRemainingApps(t *testing.T) {
	testutil.Swap(t, &verifyAllListFn, func(context.Context, string) ([]ReplicationSource, error) {
		return []ReplicationSource{{Namespace: "a", Name: "one"}, {Namespace: "b", Name: "two"}, {Namespace: "c", Name: "three"}}, nil
	})
	testutil.Swap(t, &confirmActionFn, func(string, bool) (bool, error) { return true, nil })
	now := time.Unix(100, 0)
	testutil.Swap(t, &verifyAllNowFn, func() time.Time { return now })
	var calls []string
	testutil.Swap(t, &verifyAllRunOneFn, func(_ context.Context, options verifyOptions) (verifyReport, error) {
		calls = append(calls, options.App)
		now = now.Add(6 * time.Minute)
		return verifyReport{}, nil
	})

	var output bytes.Buffer
	err := runVolsyncVerifyAll(context.Background(), verifyAllOptions{
		Timeout: 15 * time.Minute, MaxDuration: 5 * time.Minute, Output: "json",
	}, &output)
	require.NoError(t, err)
	assert.Equal(t, []string{"one"}, calls)
	assert.Contains(t, output.String(), `"result": "SKIP"`)
	assert.Contains(t, output.String(), `"skip": 2`)
	assert.Contains(t, output.String(), "budget exhausted before start")
}

func TestVerifyAllContinuesAfterFailureAndReturnsFailureExitError(t *testing.T) {
	testutil.Swap(t, &verifyAllListFn, func(context.Context, string) ([]ReplicationSource, error) {
		return []ReplicationSource{{Namespace: "apps", Name: "one"}, {Namespace: "apps", Name: "two"}, {Namespace: "apps", Name: "three"}}, nil
	})
	testutil.Swap(t, &confirmActionFn, func(string, bool) (bool, error) { return true, nil })
	var calls []string
	testutil.Swap(t, &verifyAllRunOneFn, func(_ context.Context, options verifyOptions) (verifyReport, error) {
		calls = append(calls, options.App)
		if options.App == "two" {
			return verifyReport{}, errors.New("restore exploded\nwith detail")
		}
		return verifyReport{}, nil
	})

	var output bytes.Buffer
	err := runVolsyncVerifyAll(context.Background(), verifyAllOptions{
		Timeout: time.Minute, MaxDuration: time.Hour, Output: "table",
	}, &output)
	var failure verifyAllFailuresError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, 1, failure.count)
	assert.Equal(t, []string{"one", "two", "three"}, calls)
	assert.Contains(t, output.String(), "restore exploded with detail")
	assert.Contains(t, output.String(), "PASS 2  FAIL 1  SKIP 0")
}

func TestVerifyAllExitSuccessAndConfirmationGate(t *testing.T) {
	sources := []ReplicationSource{{Namespace: "apps", Name: "one"}}
	testutil.Swap(t, &verifyAllListFn, func(context.Context, string) ([]ReplicationSource, error) { return sources, nil })
	runs := 0
	testutil.Swap(t, &verifyAllRunOneFn, func(context.Context, verifyOptions) (verifyReport, error) {
		runs++
		return verifyReport{}, nil
	})
	testutil.Swap(t, &confirmActionFn, func(string, bool) (bool, error) { return false, nil })
	err := runVolsyncVerifyAll(context.Background(), verifyAllOptions{Timeout: time.Minute, MaxDuration: time.Hour, Output: "table"}, io.Discard)
	require.ErrorContains(t, err, "cancelled")
	assert.Zero(t, runs)

	testutil.Swap(t, &confirmActionFn, func(string, bool) (bool, error) { return true, nil })
	require.NoError(t, runVolsyncVerifyAll(context.Background(), verifyAllOptions{
		Timeout: time.Minute, MaxDuration: time.Hour, Output: "table",
	}, io.Discard))
	assert.Equal(t, 1, runs)
}

func TestListVerifyAllSourcesUsesNamespaceAndSorts(t *testing.T) {
	var got string
	testutil.Swap(t, &verifyOutputFn, func(_ context.Context, args ...string) ([]byte, error) {
		got = strings.Join(args, " ")
		return []byte(`{"items":[
          {"metadata":{"name":"zeta","namespace":"media"}},
          {"metadata":{"name":"alpha","namespace":"media"}}
        ]}`), nil
	})
	sources, err := listVerifyAllSources(context.Background(), "media")
	require.NoError(t, err)
	assert.Equal(t, "get replicationsources --namespace media -o json", got)
	assert.Equal(t, []ReplicationSource{{Namespace: "media", Name: "alpha"}, {Namespace: "media", Name: "zeta"}}, sources)
}
