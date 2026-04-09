package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/yourorg/server-agent/collectors"
	"github.com/yourorg/server-agent/config"
	"github.com/yourorg/server-agent/exporters"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// ── Logger ────────────────────────────────────────────────────
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// ── Config ────────────────────────────────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Resolve hostname
	hostname := cfg.Agent.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	log.Info("agent starting", "host", hostname)

	// ── Exporters ─────────────────────────────────────────────────
	var influxExp *exporters.InfluxExporter
	if cfg.Exporter.InfluxURL != "" {
		influxExp, err = exporters.NewInfluxExporter(cfg.Exporter, log)
		if err != nil {
			log.Error("influx exporter init failed", "err", err)
			os.Exit(1)
		}
		defer influxExp.Close()
	} else {
		log.Warn("no influx_url configured — metrics will only log to stdout")
	}

	var promExp *exporters.PrometheusExporter
	if cfg.Exporter.PrometheusEnabled {
		promExp = exporters.NewPrometheusExporter(cfg.Exporter.PrometheusPort, log)
		go func() {
			if err := promExp.Serve(); err != nil {
				log.Error("prometheus server error", "err", err)
			}
		}()
	}

	// ── Collectors ────────────────────────────────────────────────
	cpuCol := collectors.NewCPUFreqCollector(hostname, log)
	numaCol := collectors.NewNUMAstatCollector(hostname, log)

	var redfishCol *collectors.RedfishCollector
	if cfg.Redfish.BMCAddress != "" {
		redfishCol = collectors.NewRedfishCollector(hostname, cfg.Redfish, log)
		log.Info("redfish collector enabled", "bmc", cfg.Redfish.BMCAddress)
	} else {
		log.Warn("redfish.bmc_address not set — Redfish collection disabled")
	}

	// ── Pipeline: collect → export ─────────────────────────────────
	emit := func(source string, metrics []collectors.Metric, collectErr error) {
		if collectErr != nil {
			log.Error("collection error", "source", source, "err", collectErr)
			return
		}
		log.Debug("collected", "source", source, "points", len(metrics))

		if influxExp != nil {
			if err := influxExp.Write(metrics); err != nil {
				log.Error("influx write error", "source", source, "err", err)
			}
		}
		if promExp != nil {
			promExp.Update(metrics)
		}
		// Always log to stdout if no exporters configured
		if influxExp == nil && promExp == nil {
			for _, m := range metrics {
				log.Info("metric", "name", m.Name, "tags", m.Tags, "fields", m.Fields)
			}
		}
	}

	// ── Scheduler ─────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// CPU Frequency — every 1s
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(cfg.Intervals.CPUFreq)
		defer tick.Stop()
		log.Info("cpufreq collector started", "interval", cfg.Intervals.CPUFreq)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m, err := cpuCol.Collect()
				emit("cpufreq", m, err)
			}
		}
	}()

	// NUMAstat — every 5s
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(cfg.Intervals.NUMAstat)
		defer tick.Stop()
		log.Info("numastat collector started", "interval", cfg.Intervals.NUMAstat)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m, err := numaCol.Collect()
				emit("numastat", m, err)
			}
		}
	}()

	// Redfish — every 15s (BMCs are slow, don't hammer them)
	if redfishCol != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(cfg.Intervals.Redfish)
			defer tick.Stop()
			log.Info("redfish collector started", "interval", cfg.Intervals.Redfish)
			// Do one immediate collection on startup
			m, err := redfishCol.Collect()
			emit("redfish", m, err)
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					m, err := redfishCol.Collect()
					emit("redfish", m, err)
				}
			}
		}()
	}

	// ── Graceful shutdown ─────────────────────────────────────────
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Println()
	log.Info("shutting down…")
	cancel()
	wg.Wait()
	log.Info("agent stopped cleanly")
}
