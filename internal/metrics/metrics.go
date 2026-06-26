package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus collectors for bollard.
type Metrics struct {
	// DNS record lifecycle
	RecordsTotal           *prometheus.CounterVec
	RecordsActive          prometheus.Gauge
	HostnameConflictsTotal prometheus.Counter
	OrphansCleanedTotal    prometheus.Counter
	MissedRecoveredTotal   prometheus.Counter

	// Reconcile loop
	ReconcileIterationsTotal *prometheus.CounterVec
	ReconcileDuration        prometheus.Histogram
	ReconcileLastTimestamp   prometheus.Gauge

	// Docker events
	DockerEventsTotal      *prometheus.CounterVec
	DockerEventErrorsTotal *prometheus.CounterVec

	// UniFi API
	UnifiRequestsTotal   *prometheus.CounterVec
	UnifiRequestDuration *prometheus.HistogramVec
	UnifiAPIVersion      *prometheus.GaugeVec

	// Process
	BuildInfo *prometheus.GaugeVec
	Up        prometheus.Gauge
}

// New creates and registers all Prometheus metrics for bollard.
func New(version, goVersion string) *Metrics {
	m := &Metrics{
		RecordsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "bollard_records_total",
			Help: "Total DNS records created, deleted, or adopted.",
		}, []string{"action", "success"}),

		RecordsActive: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "bollard_records_active",
			Help: "Number of DNS records currently owned by bollard.",
		}),

		HostnameConflictsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "bollard_record_hostname_conflicts_total",
			Help: "Total number of hostname conflicts detected.",
		}),

		OrphansCleanedTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "bollard_reconcile_orphans_cleaned_total",
			Help: "Total orphaned store records cleaned up during reconcile.",
		}),

		MissedRecoveredTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "bollard_reconcile_missed_recovered_total",
			Help: "Total missed-event recoveries performed during reconcile.",
		}),

		ReconcileIterationsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "bollard_reconcile_iterations_total",
			Help: "Total reconcile loop iterations.",
		}, []string{"status"}),

		ReconcileDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "bollard_reconcile_duration_seconds",
			Help:    "Duration of reconcile loop iterations.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
		}),

		ReconcileLastTimestamp: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "bollard_reconcile_last_timestamp_seconds",
			Help: "Unix timestamp of the last successfully completed reconcile.",
		}),

		DockerEventsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "bollard_docker_events_total",
			Help: "Total Docker container events received.",
		}, []string{"type"}),

		DockerEventErrorsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "bollard_docker_event_errors_total",
			Help: "Total errors encountered while handling Docker events.",
		}, []string{"stage"}),

		UnifiRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "bollard_unifi_requests_total",
			Help: "Total requests made to the UniFi API.",
		}, []string{"method", "status"}),

		UnifiRequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "bollard_unifi_request_duration_seconds",
			Help:    "Duration of UniFi API requests.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5},
		}, []string{"method"}),

		UnifiAPIVersion: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "bollard_unifi_api_version",
			Help: "Active UniFi API version. Set to 1 for the active provider.",
		}, []string{"provider"}),

		BuildInfo: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "bollard_build_info",
			Help: "Build information for bollard. Always 1.",
		}, []string{"version", "goversion"}),

		Up: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "bollard_up",
			Help: "1 when bollard is running normally, 0 otherwise.",
		}),
	}

	m.BuildInfo.WithLabelValues(version, goVersion).Set(1)

	return m
}
