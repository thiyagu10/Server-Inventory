package exporters

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/yourorg/server-agent/collectors"
)

// PrometheusExporter exposes collected metrics as a Prometheus /metrics endpoint.
// It stores the last value for each metric+tag combination.
type PrometheusExporter struct {
	mu      sync.RWMutex
	metrics map[string]collectors.Metric // key: name+tags
	log     *slog.Logger
	port    int
}

func NewPrometheusExporter(port int, log *slog.Logger) *PrometheusExporter {
	return &PrometheusExporter{
		metrics: make(map[string]collectors.Metric),
		log:     log,
		port:    port,
	}
}

// Update stores the latest snapshot of each metric.
func (p *PrometheusExporter) Update(metrics []collectors.Metric) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range metrics {
		p.metrics[metricKey(m)] = m
	}
}

// Serve starts the HTTP server. Call in a goroutine.
func (p *PrometheusExporter) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", p.handleMetrics)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	addr := fmt.Sprintf(":%d", p.port)
	p.log.Info("prometheus exporter listening", "addr", addr)
	return http.ListenAndServe(addr, mux)
}

func (p *PrometheusExporter) handleMetrics(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	// Group by metric name for HELP/TYPE headers
	byName := make(map[string][]collectors.Metric)
	for _, m := range p.metrics {
		byName[m.Name] = append(byName[m.Name], m)
	}

	for name, ms := range byName {
		fmt.Fprintf(w, "# HELP %s Collected by server-agent\n", promName(name))
		fmt.Fprintf(w, "# TYPE %s gauge\n", promName(name))
		for _, m := range ms {
			labelStr := promLabels(m.Tags)
			for field, val := range m.Fields {
				// Skip string fields — Prometheus only handles numbers
				numVal, ok := toFloat64(val)
				if !ok {
					continue
				}
				metricName := fmt.Sprintf("%s_%s", promName(name), promName(field))
				fmt.Fprintf(w, "%s{%s} %g %d\n",
					metricName, labelStr, numVal,
					m.Timestamp.UnixMilli(),
				)
			}
		}
	}
}

// ── Helpers ───────────────────────────────────────────────────────

func metricKey(m collectors.Metric) string {
	parts := []string{m.Name}
	for k, v := range m.Tags {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func promName(s string) string {
	return strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(s)
}

func promLabels(tags map[string]string) string {
	var parts []string
	for k, v := range tags {
		parts = append(parts, fmt.Sprintf(`%s=%q`, k, v))
	}
	return strings.Join(parts, ",")
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	default:
		return 0, false
	}
}
