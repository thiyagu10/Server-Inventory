package exporters

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"

	"github.com/yourorg/server-agent/collectors"
	"github.com/yourorg/server-agent/config"
)

// InfluxExporter writes metrics to InfluxDB v2 using the line protocol.
// It batches writes and retries on transient failures.
type InfluxExporter struct {
	client   influxdb2.Client
	writeAPI api.WriteAPIBlocking
	log      *slog.Logger
	cfg      config.ExporterConfig
}

func NewInfluxExporter(cfg config.ExporterConfig, log *slog.Logger) (*InfluxExporter, error) {
	if cfg.InfluxURL == "" {
		return nil, fmt.Errorf("influx_url is required")
	}

	client := influxdb2.NewClientWithOptions(
		cfg.InfluxURL,
		cfg.InfluxToken,
		influxdb2.DefaultOptions().
			SetBatchSize(500).
			SetFlushInterval(1000). // flush every 1s
			SetRetryInterval(5000). // retry every 5s on failure
			SetMaxRetries(3),
	)

	writeAPI := client.WriteAPIBlocking(cfg.InfluxOrg, cfg.InfluxBucket)

	log.Info("influx exporter ready",
		"url", cfg.InfluxURL,
		"org", cfg.InfluxOrg,
		"bucket", cfg.InfluxBucket,
	)

	return &InfluxExporter{
		client:   client,
		writeAPI: writeAPI,
		log:      log,
		cfg:      cfg,
	}, nil
}

// Write converts collector.Metrics to InfluxDB points and flushes them.
func (e *InfluxExporter) Write(metrics []collectors.Metric) error {
	if len(metrics) == 0 {
		return nil
	}

	points := make([]*write.Point, 0, len(metrics))
	for _, m := range metrics {
		p := influxdb2.NewPoint(m.Name, m.Tags, m.Fields, m.Timestamp)
		points = append(points, p)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.writeAPI.WritePoint(ctx, points...); err != nil {
		return fmt.Errorf("influx write: %w", err)
	}

	e.log.Debug("influx: wrote points", "count", len(points))
	return nil
}

func (e *InfluxExporter) Close() {
	e.client.Close()
}
