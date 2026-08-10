// Package metrics registers the Prometheus collectors exposed at
// /metrics: counters and histograms updated directly by
// updater/scheduler/notifier as they run, plus a store-backed collector
// for point-in-time gauges (container counts, active schedules). Naming
// follows the doupro_<noun>_<unit> convention documented in docs/stats.md
// — this is the catalogue that document describes; keep them in sync.
package metrics

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/arnaudcharles/doupro/internal/store"
)

var (
	// UpdatesTotal counts update attempts, labeled by stack and outcome.
	UpdatesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_updates_total",
		Help: "Update attempts, by stack and outcome.",
	}, []string{"stack", "status"}) // status: "success" | "failed"

	// RollbacksTotal counts rollbacks performed, labeled by stack and trigger.
	RollbacksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_rollbacks_total",
		Help: "Rollbacks performed, by stack and trigger.",
	}, []string{"stack", "trigger"}) // trigger: "auto" | "manual"

	// NotificationsTotal counts notification delivery attempts.
	NotificationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_notifications_total",
		Help: "Notification delivery attempts, by channel and outcome.",
	}, []string{"channel", "status"}) // status: "sent" | "failed"

	NotificationRetriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_notification_retries_total",
		Help: "Notification delivery retries scheduled, by channel.",
	}, []string{"channel"})

	NotificationDeadLettersTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_notification_dead_letters_total",
		Help: "Notifications moved to dead-letter after exhausting retries, by channel.",
	}, []string{"channel"})

	// UpdateDurationSeconds observes how long an update (pull -> healthy)
	// takes, labeled by stack.
	UpdateDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "doupro_update_duration_seconds",
		Help:    "Time to complete an update, from pull to healthy, by stack.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s .. ~34min
	}, []string{"stack"})

	// CheckDurationSeconds observes how long a full registry-check cycle
	// takes.
	CheckDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "doupro_check_duration_seconds",
		Help:    "Time for a full registry-check cycle across all containers.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 12),
	})

	// LastCheckTimestamp is the Unix time of the last completed check
	// cycle.
	LastCheckTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "doupro_last_check_timestamp_seconds",
		Help: "Unix time of the last completed registry-check cycle.",
	})

	ContainerOperationsActive = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "doupro_container_operations_active",
		Help: "Destructive container operations currently in progress, by operation.",
	}, []string{"operation"})

	ContainerOperationRejectionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_container_operation_rejections_total",
		Help: "Container operations rejected because another operation was already in progress.",
	}, []string{"operation"})

	SelfUpdatesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_self_updates_total",
		Help: "DoUpRo self-update requests by acceptance outcome.",
	}, []string{"status"}) // "accepted" | "refused" | "failed"

	CrashEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_crash_events_total",
		Help: "Post-update container crash events, by outcome.",
	}, []string{"outcome"})

	ScheduleExecutionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_schedule_executions_total",
		Help: "Scheduled update executions by kind, semver compatibility policy and outcome.",
	}, []string{"kind", "semver_policy", "status"})

	RegistryChecksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_registry_checks_total", Help: "Registry manifest checks by registry and outcome.",
	}, []string{"registry", "status"})
	RegistryVersionResolutionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_registry_version_resolutions_total",
		Help: "Registry-backed image version resolution attempts by registry and outcome.",
	}, []string{"registry", "status"})
	RegistryVersionCacheTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "doupro_registry_version_cache_total",
		Help: "Durable digest-to-version cache lookups by result.",
	}, []string{"result"})
)

// Registry is the collector set /metrics serves. A dedicated registry
// (not prometheus.DefaultRegisterer) keeps the exposed surface limited to
// exactly what docs/stats.md documents — no Go runtime metrics mixed in
// implicitly.
var Registry = prometheus.NewRegistry()

// Register wires every collector — the package-level counters/histograms
// above, plus a store-backed collector for point-in-time gauges — into
// Registry. Call once at startup before serving /metrics.
func Register(st *store.Store) {
	Registry.MustRegister(
		UpdatesTotal,
		RollbacksTotal,
		NotificationsTotal,
		NotificationRetriesTotal,
		NotificationDeadLettersTotal,
		UpdateDurationSeconds,
		CheckDurationSeconds,
		LastCheckTimestamp,
		ContainerOperationsActive,
		ContainerOperationRejectionsTotal,
		SelfUpdatesTotal,
		CrashEventsTotal,
		ScheduleExecutionsTotal,
		RegistryChecksTotal,
		RegistryVersionResolutionsTotal,
		RegistryVersionCacheTotal,
		newStoreCollector(st),
	)
}

// RegisterRoute mounts GET /metrics. Intentionally outside /api/v1 and
// outside auth (docs/stats.md) — standard Prometheus exporter convention;
// operators exposing DoUpRo beyond a trusted network are expected to
// gate this path at their reverse proxy the same way they would any
// other exporter (see docs/stats.md and SECURITY.md).
func RegisterRoute(mux *http.ServeMux) {
	mux.Handle("GET /metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
}

var (
	containersTotalDesc = prometheus.NewDesc(
		"doupro_containers_total", "Tracked containers.", []string{"stack"}, nil)
	containersRunningDesc = prometheus.NewDesc(
		"doupro_containers_running", "Currently running tracked containers.", []string{"stack"}, nil)
	updatesAvailableDesc = prometheus.NewDesc(
		"doupro_updates_available", "Containers with a pending update.", []string{"stack"}, nil)
	schedulesActiveDesc = prometheus.NewDesc(
		"doupro_schedules_active", "Active schedules.", []string{"kind"}, nil)
	rollbackWatchesActiveDesc = prometheus.NewDesc(
		"doupro_rollback_watches_active", "Active post-update automatic rollback watches.", nil, nil)
	notificationQueueDepthDesc = prometheus.NewDesc(
		"doupro_notification_queue_depth", "Notifications currently in the durable queue.", []string{"status"}, nil)
	unencryptedSecretsDesc = prometheus.NewDesc(
		"doupro_secrets_unencrypted", "Reversible secret values still stored without application encryption.", nil, nil)
)

// storeCollector computes the gauges that only make sense as a snapshot
// of current store state, at scrape time rather than push-updated —
// avoids every code path that touches a container having to also remember
// to update a gauge.
type storeCollector struct {
	store *store.Store
}

func newStoreCollector(st *store.Store) *storeCollector {
	return &storeCollector{store: st}
}

func (c *storeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- containersTotalDesc
	ch <- containersRunningDesc
	ch <- updatesAvailableDesc
	ch <- schedulesActiveDesc
	ch <- rollbackWatchesActiveDesc
	ch <- notificationQueueDepthDesc
	ch <- unencryptedSecretsDesc
}

func (c *storeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()
	if count, err := c.store.CountActiveRollbackWatches(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(rollbackWatchesActiveDesc, prometheus.GaugeValue, float64(count))
	}
	for _, status := range []string{"pending", "processing", "dead_letter"} {
		if count, err := c.store.CountNotificationJobs(ctx, status); err == nil {
			ch <- prometheus.MustNewConstMetric(notificationQueueDepthDesc, prometheus.GaugeValue, float64(count), status)
		}
	}
	if count, err := c.store.CountUnencryptedSecrets(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(unencryptedSecretsDesc, prometheus.GaugeValue, float64(count))
	}

	if containers, err := c.store.ListContainers(ctx); err == nil {
		byStack := map[string]struct{ total, running, available int }{}
		for _, cont := range containers {
			s := byStack[cont.Stack]
			s.total++
			if cont.State == "running" {
				s.running++
			}
			if cont.UpdateAvailable {
				s.available++
			}
			byStack[cont.Stack] = s
		}
		for stack, s := range byStack {
			ch <- prometheus.MustNewConstMetric(containersTotalDesc, prometheus.GaugeValue, float64(s.total), stack)
			ch <- prometheus.MustNewConstMetric(containersRunningDesc, prometheus.GaugeValue, float64(s.running), stack)
			ch <- prometheus.MustNewConstMetric(updatesAvailableDesc, prometheus.GaugeValue, float64(s.available), stack)
		}
	}

	if schedules, err := c.store.ListSchedules(ctx); err == nil {
		byKind := map[string]int{}
		for _, sc := range schedules {
			if sc.Enabled {
				byKind[sc.Kind]++
			}
		}
		for kind, n := range byKind {
			ch <- prometheus.MustNewConstMetric(schedulesActiveDesc, prometheus.GaugeValue, float64(n), kind)
		}
	}
}
