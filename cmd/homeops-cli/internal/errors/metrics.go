package errors

import (
	"sync"
	"time"
)

// ErrorMetrics tracks detailed error statistics
type ErrorMetrics struct {
	mu                sync.RWMutex
	errorCounts       map[ErrorType]int64
	errorsByCode      map[string]int64
	recoveryAttempts  map[ErrorType]int64
	recoverySuccesses map[ErrorType]int64
	lastErrors        map[ErrorType]*HomeOpsError
	errorTrends       map[ErrorType][]ErrorTrendPoint
	startTime         time.Time
}

// ErrorTrendPoint represents a point in error trend analysis
type ErrorTrendPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int64     `json:"count"`
	ErrorType ErrorType `json:"error_type"`
}

// ErrorReport provides comprehensive error analytics
type ErrorReport struct {
	TotalErrors      int64                           `json:"total_errors"`
	ErrorsByType     map[ErrorType]int64             `json:"errors_by_type"`
	ErrorsByCode     map[string]int64                `json:"errors_by_code"`
	RecoveryRates    map[ErrorType]float64           `json:"recovery_rates"`
	MostCommonErrors []ErrorFrequency                `json:"most_common_errors"`
	ErrorTrends      map[ErrorType][]ErrorTrendPoint `json:"error_trends"`
	Uptime           time.Duration                   `json:"uptime"`
	GeneratedAt      time.Time                       `json:"generated_at"`
}

// ErrorFrequency represents error frequency data
type ErrorFrequency struct {
	ErrorType ErrorType `json:"error_type"`
	Code      string    `json:"code"`
	Count     int64     `json:"count"`
	LastSeen  time.Time `json:"last_seen"`
}

// NewErrorMetrics creates a new error metrics collector
func NewErrorMetrics() *ErrorMetrics {
	return &ErrorMetrics{
		errorCounts:       make(map[ErrorType]int64),
		errorsByCode:      make(map[string]int64),
		recoveryAttempts:  make(map[ErrorType]int64),
		recoverySuccesses: make(map[ErrorType]int64),
		lastErrors:        make(map[ErrorType]*HomeOpsError),
		errorTrends:       make(map[ErrorType][]ErrorTrendPoint),
		startTime:         time.Now(),
	}
}

// RecordError records an error occurrence
func (em *ErrorMetrics) RecordError(err *HomeOpsError) {
	em.mu.Lock()
	defer em.mu.Unlock()

	// Increment error counts
	em.errorCounts[err.Type]++
	em.errorsByCode[err.Code]++

	// Update last error
	em.lastErrors[err.Type] = err

	// Add to trend data
	now := time.Now()
	if em.errorTrends[err.Type] == nil {
		em.errorTrends[err.Type] = make([]ErrorTrendPoint, 0)
	}

	// Add new trend point
	trendPoint := ErrorTrendPoint{
		Timestamp: now,
		Count:     em.errorCounts[err.Type],
		ErrorType: err.Type,
	}
	em.errorTrends[err.Type] = append(em.errorTrends[err.Type], trendPoint)

	// Keep only last 100 trend points per error type
	if len(em.errorTrends[err.Type]) > 100 {
		em.errorTrends[err.Type] = em.errorTrends[err.Type][1:]
	}
}

// RecordRecoveryAttempt records a recovery attempt
func (em *ErrorMetrics) RecordRecoveryAttempt(errorType ErrorType) {
	em.mu.Lock()
	defer em.mu.Unlock()

	em.recoveryAttempts[errorType]++
}

// RecordRecoverySuccess records a successful recovery
func (em *ErrorMetrics) RecordRecoverySuccess(errorType ErrorType) {
	em.mu.Lock()
	defer em.mu.Unlock()

	em.recoverySuccesses[errorType]++
}

// GetReport generates a comprehensive error report
func (em *ErrorMetrics) GetReport() *ErrorReport {
	em.mu.RLock()
	defer em.mu.RUnlock()

	report := &ErrorReport{
		ErrorsByType:     make(map[ErrorType]int64),
		ErrorsByCode:     make(map[string]int64),
		RecoveryRates:    make(map[ErrorType]float64),
		MostCommonErrors: make([]ErrorFrequency, 0),
		ErrorTrends:      make(map[ErrorType][]ErrorTrendPoint),
		Uptime:           time.Since(em.startTime),
		GeneratedAt:      time.Now(),
	}

	// Copy error counts
	var totalErrors int64
	for errorType, count := range em.errorCounts {
		report.ErrorsByType[errorType] = count
		totalErrors += count
	}
	report.TotalErrors = totalErrors

	// Copy error codes
	for code, count := range em.errorsByCode {
		report.ErrorsByCode[code] = count
	}

	// Calculate recovery rates
	for errorType, attempts := range em.recoveryAttempts {
		if attempts > 0 {
			successes := em.recoverySuccesses[errorType]
			report.RecoveryRates[errorType] = float64(successes) / float64(attempts) * 100
		}
	}

	// Find most common errors
	for errorType, count := range em.errorCounts {
		if lastErr, exists := em.lastErrors[errorType]; exists {
			lastSeen := time.Now()
			if lastErr.Context != nil {
				lastSeen = lastErr.Context.Timestamp
			}
			freq := ErrorFrequency{
				ErrorType: errorType,
				Code:      lastErr.Code,
				Count:     count,
				LastSeen:  lastSeen,
			}
			report.MostCommonErrors = append(report.MostCommonErrors, freq)
		}
	}

	// Sort most common errors by count (descending)
	for i := 0; i < len(report.MostCommonErrors)-1; i++ {
		for j := i + 1; j < len(report.MostCommonErrors); j++ {
			if report.MostCommonErrors[i].Count < report.MostCommonErrors[j].Count {
				report.MostCommonErrors[i], report.MostCommonErrors[j] =
					report.MostCommonErrors[j], report.MostCommonErrors[i]
			}
		}
	}

	// Copy trend data
	for errorType, trends := range em.errorTrends {
		report.ErrorTrends[errorType] = make([]ErrorTrendPoint, len(trends))
		copy(report.ErrorTrends[errorType], trends)
	}

	return report
}

// GetErrorRate returns the error rate for a specific time window
func (em *ErrorMetrics) GetErrorRate(errorType ErrorType, window time.Duration) float64 {
	em.mu.RLock()
	defer em.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-window)

	trends, exists := em.errorTrends[errorType]
	if !exists {
		return 0.0
	}

	var recentErrors int64
	for _, trend := range trends {
		if trend.Timestamp.After(cutoff) {
			recentErrors++
		}
	}

	// Calculate rate per minute
	minutes := window.Minutes()
	if minutes == 0 {
		return 0.0
	}

	return float64(recentErrors) / minutes
}

// Reset resets all metrics
func (em *ErrorMetrics) Reset() {
	em.mu.Lock()
	defer em.mu.Unlock()

	em.errorCounts = make(map[ErrorType]int64)
	em.errorsByCode = make(map[string]int64)
	em.recoveryAttempts = make(map[ErrorType]int64)
	em.recoverySuccesses = make(map[ErrorType]int64)
	em.lastErrors = make(map[ErrorType]*HomeOpsError)
	em.errorTrends = make(map[ErrorType][]ErrorTrendPoint)
	em.startTime = time.Now()
}

// HealthStatus represents the overall health status
type HealthStatus struct {
	Status          string              `json:"status"`
	ErrorRate       float64             `json:"error_rate"`
	RecoveryRate    float64             `json:"recovery_rate"`
	CriticalErrors  int64               `json:"critical_errors"`
	RecentErrors    map[ErrorType]int64 `json:"recent_errors"`
	HealthScore     float64             `json:"health_score"`
	Recommendations []string            `json:"recommendations"`
	LastUpdated     time.Time           `json:"last_updated"`
}

// GetHealthStatus returns the current health status
func (em *ErrorMetrics) GetHealthStatus() *HealthStatus {
	em.mu.RLock()
	defer em.mu.RUnlock()

	status := &HealthStatus{
		RecentErrors:    make(map[ErrorType]int64),
		Recommendations: make([]string, 0),
		LastUpdated:     time.Now(),
	}

	// Calculate recent errors (last 5 minutes)
	recentWindow := 5 * time.Minute
	now := time.Now()
	cutoff := now.Add(-recentWindow)

	var totalRecentErrors int64
	var totalRecoveryAttempts int64
	var totalRecoverySuccesses int64

	for errorType, trends := range em.errorTrends {
		var recentCount int64
		for _, trend := range trends {
			if trend.Timestamp.After(cutoff) {
				recentCount++
			}
		}
		status.RecentErrors[errorType] = recentCount
		totalRecentErrors += recentCount
	}

	// Calculate overall recovery rate
	for _, attempts := range em.recoveryAttempts {
		totalRecoveryAttempts += attempts
	}
	for _, successes := range em.recoverySuccesses {
		totalRecoverySuccesses += successes
	}

	if totalRecoveryAttempts > 0 {
		status.RecoveryRate = float64(totalRecoverySuccesses) / float64(totalRecoveryAttempts) * 100
	} else {
		// No recovery attempts means no failures to recover from
		status.RecoveryRate = 100.0
	}

	// Calculate error rate (errors per minute)
	status.ErrorRate = float64(totalRecentErrors) / recentWindow.Minutes()

	// Count critical errors (security, validation)
	status.CriticalErrors = em.errorCounts[ErrTypeSecurity] + em.errorCounts[ErrTypeValidation]

	// Calculate health score (0-100)
	healthScore := 100.0

	// Deduct points for high error rate
	if status.ErrorRate > 10 {
		healthScore -= 30
		status.Recommendations = append(status.Recommendations,
			"High error rate detected. Consider investigating root causes.")
	} else if status.ErrorRate > 5 {
		healthScore -= 15
		status.Recommendations = append(status.Recommendations,
			"Moderate error rate. Monitor for trends.")
	}

	// Deduct points for low recovery rate (only if there were recovery attempts)
	if totalRecoveryAttempts > 0 {
		if status.RecoveryRate < 50 {
			healthScore -= 25
			status.Recommendations = append(status.Recommendations,
				"Low recovery rate. Review retry strategies and error handling.")
		} else if status.RecoveryRate < 80 {
			healthScore -= 10
		}
	}

	// Deduct points for critical errors
	if status.CriticalErrors > 0 {
		healthScore -= 20
		status.Recommendations = append(status.Recommendations,
			"Critical errors detected. Immediate attention required.")
	}

	// Ensure health score is within bounds
	if healthScore < 0 {
		healthScore = 0
	}
	status.HealthScore = healthScore

	// Determine overall status
	switch {
	case healthScore >= 90:
		status.Status = "excellent"
	case healthScore >= 75:
		status.Status = "good"
	case healthScore >= 50:
		status.Status = "fair"
	case healthScore >= 25:
		status.Status = "poor"
	default:
		status.Status = "critical"
	}

	if len(status.Recommendations) == 0 {
		status.Recommendations = append(status.Recommendations, "System is operating normally.")
	}

	return status
}

// ErrorAlert represents an alert condition
type ErrorAlert struct {
	ID           string    `json:"id"`
	Severity     string    `json:"severity"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	ErrorType    ErrorType `json:"error_type"`
	Threshold    float64   `json:"threshold"`
	CurrentValue float64   `json:"current_value"`
	TriggeredAt  time.Time `json:"triggered_at"`
}

// AlertManager manages error alerts and thresholds
type AlertManager struct {
	mu         sync.RWMutex
	thresholds map[string]float64
	alerts     []*ErrorAlert
	metrics    *ErrorMetrics
}

// NewAlertManager creates a new alert manager
func NewAlertManager(metrics *ErrorMetrics) *AlertManager {
	am := &AlertManager{
		thresholds: make(map[string]float64),
		alerts:     make([]*ErrorAlert, 0),
		metrics:    metrics,
	}

	// Set default thresholds
	am.thresholds["error_rate"] = 5.0      // errors per minute
	am.thresholds["recovery_rate"] = 80.0  // percentage
	am.thresholds["critical_errors"] = 1.0 // count

	return am
}

// SetThreshold sets an alert threshold
func (am *AlertManager) SetThreshold(metric string, threshold float64) {
	am.mu.Lock()
	defer am.mu.Unlock()

	am.thresholds[metric] = threshold
}

// CheckAlerts checks for alert conditions and returns any triggered alerts
func (am *AlertManager) CheckAlerts() []*ErrorAlert {
	am.mu.Lock()
	defer am.mu.Unlock()

	newAlerts := make([]*ErrorAlert, 0)
	health := am.metrics.GetHealthStatus()

	// Check error rate threshold
	if threshold, exists := am.thresholds["error_rate"]; exists {
		if health.ErrorRate > threshold {
			alert := &ErrorAlert{
				ID:           "high_error_rate",
				Severity:     "warning",
				Title:        "High Error Rate",
				Description:  "Error rate has exceeded the configured threshold",
				Threshold:    threshold,
				CurrentValue: health.ErrorRate,
				TriggeredAt:  time.Now(),
			}
			newAlerts = append(newAlerts, alert)
		}
	}

	// Check recovery rate threshold
	if threshold, exists := am.thresholds["recovery_rate"]; exists {
		if health.RecoveryRate < threshold {
			alert := &ErrorAlert{
				ID:           "low_recovery_rate",
				Severity:     "warning",
				Title:        "Low Recovery Rate",
				Description:  "Recovery rate has fallen below the configured threshold",
				Threshold:    threshold,
				CurrentValue: health.RecoveryRate,
				TriggeredAt:  time.Now(),
			}
			newAlerts = append(newAlerts, alert)
		}
	}

	// Check critical errors threshold
	if threshold, exists := am.thresholds["critical_errors"]; exists {
		if float64(health.CriticalErrors) >= threshold {
			alert := &ErrorAlert{
				ID:           "critical_errors",
				Severity:     "critical",
				Title:        "Critical Errors Detected",
				Description:  "Critical security or validation errors have been detected",
				Threshold:    threshold,
				CurrentValue: float64(health.CriticalErrors),
				TriggeredAt:  time.Now(),
			}
			newAlerts = append(newAlerts, alert)
		}
	}

	// Add new alerts to the list
	am.alerts = append(am.alerts, newAlerts...)

	// Keep only last 50 alerts
	if len(am.alerts) > 50 {
		am.alerts = am.alerts[len(am.alerts)-50:]
	}

	return newAlerts
}

// GetActiveAlerts returns all active alerts
func (am *AlertManager) GetActiveAlerts() []*ErrorAlert {
	am.mu.RLock()
	defer am.mu.RUnlock()

	// Return alerts from the last hour
	cutoff := time.Now().Add(-time.Hour)
	activeAlerts := make([]*ErrorAlert, 0)

	for _, alert := range am.alerts {
		if alert.TriggeredAt.After(cutoff) {
			activeAlerts = append(activeAlerts, alert)
		}
	}

	return activeAlerts
}
