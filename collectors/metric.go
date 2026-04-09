package collectors

import "time"

// Metric is a single measurement point emitted by any collector.
type Metric struct {
	Name      string            // e.g. "cpu_frequency_mhz"
	Tags      map[string]string // e.g. {"cpu": "0", "host": "srv01"}
	Fields    map[string]any    // e.g. {"cur_mhz": 3400.0, "min_mhz": 800.0}
	Timestamp time.Time
}
