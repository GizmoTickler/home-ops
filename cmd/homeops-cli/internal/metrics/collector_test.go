package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformanceCollectorTrackAndReport(t *testing.T) {
	collector := NewPerformanceCollector()

	require.NoError(t, collector.TrackOperation("render", func() error {
		time.Sleep(2 * time.Millisecond)
		return nil
	}))

	expectedErr := errors.New("boom")
	err := collector.TrackOperation("render", func() error {
		time.Sleep(1 * time.Millisecond)
		return expectedErr
	})
	require.ErrorIs(t, err, expectedErr)

	report, ok := collector.GetOperationReport("render")
	require.True(t, ok)
	assert.Equal(t, int64(2), report.TotalCalls)
	assert.Equal(t, 0.5, report.ErrorRate)
	assert.NotZero(t, report.LastExecution)
	assert.GreaterOrEqual(t, report.MaxDuration, report.MinDuration)
	assert.Greater(t, report.AverageDuration, time.Duration(0))

	all := collector.GetReport()
	require.Contains(t, all, "render")
	assert.Equal(t, report.TotalCalls, all["render"].TotalCalls)

	assert.Contains(t, collector.GetOperationNames(), "render")
	assert.Equal(t, 1, collector.GetTotalOperations())

	collector.Reset()
	assert.Equal(t, 0, collector.GetTotalOperations())
	_, ok = collector.GetOperationReport("render")
	assert.False(t, ok)
}

func TestPerformanceCollectorTrackOperationWithResult(t *testing.T) {
	collector := NewPerformanceCollector()

	result, err := collector.TrackOperationWithResult("discover", func() (interface{}, error) {
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)

	report, ok := collector.GetOperationReport("discover")
	require.True(t, ok)
	assert.Equal(t, int64(1), report.TotalCalls)
	assert.Equal(t, 0.0, report.ErrorRate)
}
