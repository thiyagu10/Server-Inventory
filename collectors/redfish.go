package collectors

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/yourorg/server-agent/config"
)

// RedfishCollector polls the BMC Redfish REST API for power metrics.
// Metrics emitted:
//   - redfish_power  fields: system_power_w, dram_power_w, cpu_power_w,
//                            psu_input_w, psu_output_w, psu_efficiency_pct
//     tags: host=<h>, chassis=<id>
//   - redfish_thermal fields: inlet_temp_c, outlet_temp_c, cpu_temp_c
//     tags: host=<h>, sensor=<name>
type RedfishCollector struct {
	cfg    config.RedfishConfig
	host   string
	log    *slog.Logger
	client *http.Client
	base   string // e.g. https://192.168.1.10
}

func NewRedfishCollector(host string, cfg config.RedfishConfig, log *slog.Logger) *RedfishCollector {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !cfg.TLSVerify},
	}
	return &RedfishCollector{
		cfg:  cfg,
		host: host,
		log:  log,
		client: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: transport,
		},
		base: fmt.Sprintf("https://%s", cfg.BMCAddress),
	}
}

func (r *RedfishCollector) Collect() ([]Metric, error) {
	now := time.Now()
	var metrics []Metric

	// ── Power metrics ─────────────────────────────────────────────
	powerMetrics, err := r.collectPower(now)
	if err != nil {
		r.log.Warn("redfish: power collection failed", "err", err)
	} else {
		metrics = append(metrics, powerMetrics...)
	}

	// ── Thermal metrics ───────────────────────────────────────────
	thermalMetrics, err := r.collectThermal(now)
	if err != nil {
		r.log.Warn("redfish: thermal collection failed", "err", err)
	} else {
		metrics = append(metrics, thermalMetrics...)
	}

	r.log.Debug("redfish: collected", "metrics", len(metrics))
	return metrics, nil
}

// ── Power ─────────────────────────────────────────────────────────

// Redfish Power response (DMTF DSP0268)
type redfishPowerResponse struct {
	PowerControl []struct {
		Name                 string  `json:"Name"`
		PowerConsumedWatts   float64 `json:"PowerConsumedWatts"`
		PowerCapacityWatts   float64 `json:"PowerCapacityWatts"`
		PowerMetrics         *struct {
			MinConsumedWatts     float64 `json:"MinConsumedWatts"`
			MaxConsumedWatts     float64 `json:"MaxConsumedWatts"`
			AverageConsumedWatts float64 `json:"AverageConsumedWatts"`
		} `json:"PowerMetrics"`
		Oem *struct {
			// Dell/HPE/Lenovo OEM extensions for DRAM/CPU power
			DellPower *struct {
				SystemInputPowerWatts float64 `json:"SystemInputPowerWatts"`
			} `json:"Dell"`
			Hpe *struct {
				PowerDetail []struct {
					Name   string  `json:"Name"`
					Watts  float64 `json:"Watts"`
				} `json:"PowerDetail"`
			} `json:"Hpe"`
		} `json:"Oem"`
	} `json:"PowerControl"`

	PowerSupplies []struct {
		Name                string  `json:"Name"`
		PowerInputWatts     float64 `json:"PowerInputWatts"`
		PowerOutputWatts    float64 `json:"PowerOutputWatts"`
		PowerCapacityWatts  float64 `json:"PowerCapacityWatts"`
		EfficiencyPercent   float64 `json:"EfficiencyPercent"`
		Status              struct {
			State  string `json:"State"`
			Health string `json:"Health"`
		} `json:"Status"`
	} `json:"PowerSupplies"`

	Voltages []struct {
		Name                  string  `json:"Name"`
		ReadingVolts          float64 `json:"ReadingVolts"`
		UpperThresholdCritical float64 `json:"UpperThresholdCritical"`
	} `json:"Voltages"`
}

func (r *RedfishCollector) collectPower(now time.Time) ([]Metric, error) {
	// Standard Redfish path — works on Dell iDRAC, HPE iLO, Lenovo XCC, Supermicro
	path := "/redfish/v1/Chassis/System.Embedded.1/Power"
	var payload redfishPowerResponse
	if err := r.get(path, &payload); err != nil {
		// Try generic path as fallback
		path = "/redfish/v1/Chassis/1/Power"
		if err2 := r.get(path, &payload); err2 != nil {
			return nil, fmt.Errorf("power endpoint: %w (tried both paths)", err)
		}
	}

	var metrics []Metric

	// ── System-level power (PowerControl[0] = whole system) ───────
	for i, pc := range payload.PowerControl {
		tags := map[string]string{
			"host":    r.host,
			"control": fmt.Sprintf("%d", i),
			"name":    pc.Name,
		}
		fields := map[string]any{
			"system_power_w":   pc.PowerConsumedWatts,
			"capacity_w":       pc.PowerCapacityWatts,
		}
		if pm := pc.PowerMetrics; pm != nil {
			fields["power_avg_w"] = pm.AverageConsumedWatts
			fields["power_min_w"] = pm.MinConsumedWatts
			fields["power_max_w"] = pm.MaxConsumedWatts
		}

		// ── OEM HPE: extract DRAM / CPU / GPU power detail ────────
		if pc.Oem != nil && pc.Oem.Hpe != nil {
			for _, detail := range pc.Oem.Hpe.PowerDetail {
				switch detail.Name {
				case "DRAM", "Memory":
					fields["dram_power_w"] = detail.Watts
				case "CPU", "Processor":
					fields["cpu_power_w"] = detail.Watts
				case "GPU":
					fields["gpu_power_w"] = detail.Watts
				case "Storage":
					fields["storage_power_w"] = detail.Watts
				}
			}
		}

		metrics = append(metrics, Metric{
			Name:      "redfish_power",
			Tags:      tags,
			Fields:    fields,
			Timestamp: now,
		})
	}

	// ── PSU metrics ───────────────────────────────────────────────
	for i, psu := range payload.PowerSupplies {
		tags := map[string]string{
			"host": r.host,
			"psu":  fmt.Sprintf("%d", i),
			"name": psu.Name,
		}
		metrics = append(metrics, Metric{
			Name: "redfish_psu",
			Tags: tags,
			Fields: map[string]any{
				"input_w":         psu.PowerInputWatts,
				"output_w":        psu.PowerOutputWatts,
				"capacity_w":      psu.PowerCapacityWatts,
				"efficiency_pct":  psu.EfficiencyPercent,
				"state":           psu.Status.State,
				"health":          psu.Status.Health,
			},
			Timestamp: now,
		})
	}

	// ── Voltage rails ─────────────────────────────────────────────
	for _, v := range payload.Voltages {
		metrics = append(metrics, Metric{
			Name: "redfish_voltage",
			Tags: map[string]string{"host": r.host, "rail": v.Name},
			Fields: map[string]any{
				"reading_v":   v.ReadingVolts,
				"critical_v":  v.UpperThresholdCritical,
			},
			Timestamp: now,
		})
	}

	return metrics, nil
}

// ── Thermal ───────────────────────────────────────────────────────

type redfishThermalResponse struct {
	Temperatures []struct {
		Name                   string  `json:"Name"`
		ReadingCelsius         float64 `json:"ReadingCelsius"`
		UpperThresholdCritical float64 `json:"UpperThresholdCritical"`
		UpperThresholdFatal    float64 `json:"UpperThresholdFatal"`
		PhysicalContext        string  `json:"PhysicalContext"`
		Status                 struct {
			State  string `json:"State"`
			Health string `json:"Health"`
		} `json:"Status"`
	} `json:"Temperatures"`
	Fans []struct {
		Name          string  `json:"Name"`
		Reading       float64 `json:"Reading"`
		ReadingUnits  string  `json:"ReadingUnits"`
	} `json:"Fans"`
}

func (r *RedfishCollector) collectThermal(now time.Time) ([]Metric, error) {
	path := "/redfish/v1/Chassis/System.Embedded.1/Thermal"
	var payload redfishThermalResponse
	if err := r.get(path, &payload); err != nil {
		path = "/redfish/v1/Chassis/1/Thermal"
		if err2 := r.get(path, &payload); err2 != nil {
			return nil, fmt.Errorf("thermal endpoint: %w", err)
		}
	}

	var metrics []Metric

	for _, t := range payload.Temperatures {
		if t.Status.State != "Enabled" {
			continue
		}
		metrics = append(metrics, Metric{
			Name: "redfish_thermal",
			Tags: map[string]string{
				"host":    r.host,
				"sensor":  t.Name,
				"context": t.PhysicalContext,
			},
			Fields: map[string]any{
				"temp_c":          t.ReadingCelsius,
				"critical_c":      t.UpperThresholdCritical,
				"fatal_c":         t.UpperThresholdFatal,
				"health":          t.Status.Health,
			},
			Timestamp: now,
		})
	}

	for _, fan := range payload.Fans {
		metrics = append(metrics, Metric{
			Name: "redfish_fan",
			Tags: map[string]string{"host": r.host, "fan": fan.Name},
			Fields: map[string]any{
				"reading":       fan.Reading,
				"reading_units": fan.ReadingUnits,
			},
			Timestamp: now,
		})
	}

	return metrics, nil
}

// ── HTTP helper ───────────────────────────────────────────────────

func (r *RedfishCollector) get(path string, out any) error {
	req, err := http.NewRequest("GET", r.base+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
	req.Header.Set("Accept", "application/json")
	// Redfish standard header
	req.Header.Set("OData-Version", "4.0")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, body)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}
