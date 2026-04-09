package collectors

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const numaBase = "/sys/devices/system/node"

// NUMAstatCollector reads per-NUMA-node memory statistics from sysfs.
// Metrics emitted:
//   - numastat  fields: mem_total_mb, mem_free_mb, mem_used_mb,
//                       mem_active_mb, mem_inactive_mb,
//                       numa_hit, numa_miss, numa_foreign,
//                       interleave_hit, local_node, other_node
//     tags: host=<h>, node=<N>
type NUMAstatCollector struct {
	log  *slog.Logger
	host string
}

func NewNUMAstatCollector(host string, log *slog.Logger) *NUMAstatCollector {
	return &NUMAstatCollector{log: log, host: host}
}

func (c *NUMAstatCollector) Collect() ([]Metric, error) {
	nodes, err := filepath.Glob(filepath.Join(numaBase, "node[0-9]*"))
	if err != nil || len(nodes) == 0 {
		return nil, fmt.Errorf("numastat: no node dirs under %s", numaBase)
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		metrics []Metric
		now     = time.Now()
	)

	for _, nodeDir := range nodes {
		wg.Add(1)
		go func(nodeDir string) {
			defer wg.Done()

			nodeID := strings.TrimPrefix(filepath.Base(nodeDir), "node")
			tags := map[string]string{"host": c.host, "node": nodeID}

			fields := make(map[string]any)

			// ── /sys/devices/system/node/nodeN/meminfo ────────────────
			memFields, err := parseNodeMeminfo(filepath.Join(nodeDir, "meminfo"), nodeID)
			if err != nil {
				c.log.Warn("numastat: meminfo", "node", nodeID, "err", err)
			} else {
				for k, v := range memFields {
					fields[k] = v
				}
			}

			// ── /sys/devices/system/node/nodeN/numastat ───────────────
			numaFields, err := parseNumastat(filepath.Join(nodeDir, "numastat"))
			if err != nil {
				c.log.Warn("numastat: numastat file", "node", nodeID, "err", err)
			} else {
				for k, v := range numaFields {
					fields[k] = v
				}
			}

			if len(fields) == 0 {
				return
			}

			mu.Lock()
			metrics = append(metrics, Metric{
				Name:      "numastat",
				Tags:      tags,
				Fields:    fields,
				Timestamp: now,
			})
			mu.Unlock()
		}(nodeDir)
	}

	wg.Wait()
	c.log.Debug("numastat: collected", "nodes", len(nodes))
	return metrics, nil
}

// parseNodeMeminfo parses /sys/devices/system/node/nodeN/meminfo
// Format: "Node 0 MemTotal:       32768000 kB"
func parseNodeMeminfo(path, nodeID string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fields := make(map[string]any)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// e.g. "Node 0 MemTotal:       32768000 kB"
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		key := strings.TrimSuffix(parts[2], ":")
		val, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			continue
		}
		mb := float64(val) / 1024.0

		switch key {
		case "MemTotal":
			fields["mem_total_mb"] = mb
		case "MemFree":
			fields["mem_free_mb"] = mb
			if total, ok := fields["mem_total_mb"].(float64); ok {
				fields["mem_used_mb"] = total - mb
			}
		case "Active":
			fields["mem_active_mb"] = mb
		case "Inactive":
			fields["mem_inactive_mb"] = mb
		case "Dirty":
			fields["mem_dirty_mb"] = mb
		case "HugePages_Total":
			fields["hugepages_total"] = val
		case "HugePages_Free":
			fields["hugepages_free"] = val
		}
	}
	return fields, scanner.Err()
}

// parseNumastat parses /sys/devices/system/node/nodeN/numastat
// Format: "numa_hit 12345678"
func parseNumastat(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fields := make(map[string]any)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) != 2 {
			continue
		}
		val, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		// Keys: numa_hit, numa_miss, numa_foreign, interleave_hit, local_node, other_node
		fields[parts[0]] = val
	}
	return fields, scanner.Err()
}
