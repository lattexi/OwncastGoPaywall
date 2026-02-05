package handlers

import (
	"net/http"

	"github.com/laurikarhu/stream-paywall/internal/metrics"
)

// MetricsHandler handles metrics API requests
type MetricsHandler struct {
	collector *metrics.Collector
}

// NewMetricsHandler creates a new metrics handler
func NewMetricsHandler(collector *metrics.Collector) *MetricsHandler {
	return &MetricsHandler{
		collector: collector,
	}
}

// GetMetrics returns current system metrics as JSON
func (h *MetricsHandler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	systemMetrics, err := h.collector.Collect(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to collect metrics")
		return
	}

	writeJSON(w, http.StatusOK, systemMetrics)
}
