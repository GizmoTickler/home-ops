package metrics

import (
	"sync"
	"time"
)

// PerformanceCollector tracks operation metrics
type PerformanceCollector struct {
	operations map[string]*OperationMetrics
	mu         sync.RWMutex
}

// OperationMetrics holds metrics for a specific operation
type OperationMetrics struct {
	TotalCalls    int64
	TotalDuration time.Duration
	Errors        int64
	LastExecution time.Time
	MinDuration   time.Duration
	MaxDuration   time.Duration
}

// OperationReport provides a summary of operation performance
type OperationReport struct {
	AverageDuration time.Duration `json:"average_duration"`
	MinDuration     time.Duration `json:"min_duration"`
	MaxDuration     time.Duration `json:"max_duration"`
	TotalCalls      int64         `json:"total_calls"`
	ErrorRate       float64       `json:"error_rate"`
	LastExecution   time.Time     `json:"last_execution"`
}

// NewPerformanceCollector creates a new performance collector
func NewPerformanceCollector() *PerformanceCollector {
	return &PerformanceCollector{
		operations: make(map[string]*OperationMetrics),
	}
}

// TrackOperation executes a function and tracks its performance
func (pc *PerformanceCollector) TrackOperation(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	duration := time.Since(start)

	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.operations[name] == nil {
		pc.operations[name] = &OperationMetrics{
			MinDuration: duration,
			MaxDuration: duration,
		}
	}

	metrics := pc.operations[name]
	metrics.TotalCalls++
	metrics.TotalDuration += duration
	metrics.LastExecution = start

	// Update min/max durations
	if duration < metrics.MinDuration {
		metrics.MinDuration = duration
	}
	if duration > metrics.MaxDuration {
		metrics.MaxDuration = duration
	}

	if err != nil {
		metrics.Errors++
	}

	return err
}

// TrackOperationWithResult executes a function that returns a result and tracks its performance
func (pc *PerformanceCollector) TrackOperationWithResult(name string, fn func() (interface{}, error)) (interface{}, error) {
	var result interface{}
	err := pc.TrackOperation(name, func() error {
		var fnErr error
		result, fnErr = fn()
		return fnErr
	})
	return result, err
}

// GetReport returns a performance report for all tracked operations
func (pc *PerformanceCollector) GetReport() map[string]OperationReport {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	report := make(map[string]OperationReport)
	for name, metrics := range pc.operations {
		avgDuration := time.Duration(0)
		if metrics.TotalCalls > 0 {
			avgDuration = metrics.TotalDuration / time.Duration(metrics.TotalCalls)
		}

		errorRate := float64(0)
		if metrics.TotalCalls > 0 {
			errorRate = float64(metrics.Errors) / float64(metrics.TotalCalls)
		}

		report[name] = OperationReport{
			AverageDuration: avgDuration,
			MinDuration:     metrics.MinDuration,
			MaxDuration:     metrics.MaxDuration,
			TotalCalls:      metrics.TotalCalls,
			ErrorRate:       errorRate,
			LastExecution:   metrics.LastExecution,
		}
	}

	return report
}

// GetOperationReport returns a performance report for a specific operation
func (pc *PerformanceCollector) GetOperationReport(name string) (OperationReport, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	metrics, exists := pc.operations[name]
	if !exists {
		return OperationReport{}, false
	}

	avgDuration := time.Duration(0)
	if metrics.TotalCalls > 0 {
		avgDuration = metrics.TotalDuration / time.Duration(metrics.TotalCalls)
	}

	errorRate := float64(0)
	if metrics.TotalCalls > 0 {
		errorRate = float64(metrics.Errors) / float64(metrics.TotalCalls)
	}

	return OperationReport{
		AverageDuration: avgDuration,
		MinDuration:     metrics.MinDuration,
		MaxDuration:     metrics.MaxDuration,
		TotalCalls:      metrics.TotalCalls,
		ErrorRate:       errorRate,
		LastExecution:   metrics.LastExecution,
	}, true
}

// Reset clears all collected metrics
func (pc *PerformanceCollector) Reset() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.operations = make(map[string]*OperationMetrics)
}

// GetOperationNames returns a list of all tracked operation names
func (pc *PerformanceCollector) GetOperationNames() []string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	names := make([]string, 0, len(pc.operations))
	for name := range pc.operations {
		names = append(names, name)
	}
	return names
}

// GetTotalOperations returns the total number of operations tracked
func (pc *PerformanceCollector) GetTotalOperations() int {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return len(pc.operations)
}
