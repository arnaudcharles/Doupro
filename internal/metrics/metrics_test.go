package metrics

import "testing"

// TestStackLabeledMetricsAcceptTheDocumentedLabels guards docs/stats.md's
// per-stack breakdown promise on these three metrics: WithLabelValues
// panics at runtime (not compile time — it takes a variadic string list)
// on a label-count mismatch between a CounterVec/HistogramVec's
// declaration here and a call site in internal/updater, so this exercises
// the exact label shapes those call sites use.
func TestStackLabeledMetricsAcceptTheDocumentedLabels(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("WithLabelValues panicked: %v", r)
		}
	}()
	UpdatesTotal.WithLabelValues("business", "success").Inc()
	UpdatesTotal.WithLabelValues("business", "failed").Inc()
	RollbacksTotal.WithLabelValues("business", "auto").Inc()
	RollbacksTotal.WithLabelValues("business", "manual").Inc()
	UpdateDurationSeconds.WithLabelValues("business").Observe(1.5)
}
