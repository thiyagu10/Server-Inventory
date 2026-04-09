package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Agent    AgentConfig    `yaml:"agent"`
	Redfish  RedfishConfig  `yaml:"redfish"`
	Exporter ExporterConfig `yaml:"exporter"`
	Intervals IntervalConfig `yaml:"intervals"`
}

type AgentConfig struct {
	Hostname   string `yaml:"hostname"`   // override; defaults to os.Hostname()
	LogLevel   string `yaml:"log_level"`  // debug | info | warn | error
}

type RedfishConfig struct {
	BMCAddress string `yaml:"bmc_address"` // e.g. 192.168.1.10
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	TLSVerify  bool   `yaml:"tls_verify"`  // set false for self-signed BMC certs
	Timeout    time.Duration `yaml:"timeout"`
}

type ExporterConfig struct {
	// InfluxDB v2
	InfluxURL    string `yaml:"influx_url"`    // http://influxdb:8086
	InfluxToken  string `yaml:"influx_token"`
	InfluxOrg    string `yaml:"influx_org"`
	InfluxBucket string `yaml:"influx_bucket"`

	// Prometheus (optional — exposes /metrics endpoint)
	PrometheusEnabled bool   `yaml:"prometheus_enabled"`
	PrometheusPort    int    `yaml:"prometheus_port"` // default 9200
}

type IntervalConfig struct {
	CPUFreq  time.Duration `yaml:"cpufreq"`   // default 1s
	NUMAstat time.Duration `yaml:"numastat"`  // default 5s
	Redfish  time.Duration `yaml:"redfish"`   // default 15s (BMCs are slow)
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// Expand environment variables in config values
	data = []byte(os.ExpandEnv(string(data)))

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Agent.LogLevel == "" {
		c.Agent.LogLevel = "info"
	}
	if c.Redfish.Timeout == 0 {
		c.Redfish.Timeout = 10 * time.Second
	}
	if c.Exporter.PrometheusPort == 0 {
		c.Exporter.PrometheusPort = 9200
	}
	if c.Intervals.CPUFreq == 0 {
		c.Intervals.CPUFreq = 1 * time.Second
	}
	if c.Intervals.NUMAstat == 0 {
		c.Intervals.NUMAstat = 5 * time.Second
	}
	if c.Intervals.Redfish == 0 {
		c.Intervals.Redfish = 15 * time.Second
	}
}
