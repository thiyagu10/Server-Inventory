package collectors

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const cpufreqBase = "/sys/devices/system/cpu"

// CPUFreqCollector reads per-core frequency and governor info from sysfs.
// Metrics emitted:
//   - cpu_frequency  fields: cur_mhz, min_mhz, max_mhz   tags: cpu=<N>
//   - cpu_governor   fields: governor=<string>             tags: cpu=<N>
type CPUFreqCollector struct {
	log  *slog.Logger
	host string
}

func NewCPUFreqCollector(host string, log *slog.Logger) *CPUFreqCollector {
	return &CPUFreqCollector{log: log, host: host}
}

// Collect reads all cpuN/cpufreq directories concurrently and returns metrics.
func (c *CPUFreqCollector) Collect() ([]Metric, error) {
	cores, err := filepath.Glob(filepath.Join(cpufreqBase, "cpu[0-9]*", "cpufreq"))
	if err != nil || len(cores) == 0 {
		return nil, fmt.Errorf("cpufreq: no cpu dirs found under %s", cpufreqBase)
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		metrics []Metric
		now     = time.Now()
	)

	for _, dir := range cores {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()

			// Extract cpu index from path e.g. /sys/.../cpu3/cpufreq → "3"
			cpuID := filepath.Base(filepath.Dir(dir))
			cpuNum := strings.TrimPrefix(cpuID, "cpu")

			cur, err := readKHz(filepath.Join(dir, "scaling_cur_freq"))
			if err != nil {
				c.log.Warn("cpufreq: read cur_freq", "cpu", cpuNum, "err", err)
				return
			}
			min, _ := readKHz(filepath.Join(dir, "scaling_min_freq"))
			max, _ := readKHz(filepath.Join(dir, "scaling_max_freq"))
			gov, _ := readString(filepath.Join(dir, "scaling_governor"))
			driver, _ := readString(filepath.Join(dir, "scaling_driver"))

			tags := map[string]string{
				"host": c.host,
				"cpu":  cpuNum,
			}

			freqMetric := Metric{
				Name:      "cpu_frequency",
				Tags:      tags,
				Fields: map[string]any{
					"cur_mhz": kHzToMHz(cur),
					"min_mhz": kHzToMHz(min),
					"max_mhz": kHzToMHz(max),
					"driver":  driver,
				},
				Timestamp: now,
			}
			govMetric := Metric{
				Name:      "cpu_governor",
				Tags:      tags,
				Fields:    map[string]any{"governor": gov},
				Timestamp: now,
			}

			mu.Lock()
			metrics = append(metrics, freqMetric, govMetric)
			mu.Unlock()
		}(dir)
	}

	wg.Wait()
	c.log.Debug("cpufreq: collected", "cores", len(cores))
	return metrics, nil
}

// ── Helpers ───────────────────────────────────────────────────────

func readKHz(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}

func readString(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func kHzToMHz(khz int64) float64 {
	return float64(khz) / 1000.0
}
