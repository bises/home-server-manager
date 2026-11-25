package hostmetrics

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/bises/home-server-manager/internal/config"
)

// MemoryMetrics holds host RAM statistics in bytes.
type MemoryMetrics struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
}

// LoadMetrics holds 1, 5, and 15 minute load averages.
type LoadMetrics struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// DiskMetric holds filesystem usage in bytes for one configured mount.
type DiskMetric struct {
	ID             string  `json:"id"`
	Label          string  `json:"label"`
	Path           string  `json:"path"`
	Available      bool    `json:"available"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	FreeBytes      uint64  `json:"free_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsedPercent    float64 `json:"used_percent"`
	Error          string  `json:"error,omitempty"`
}

// HostMetrics aggregates allowlisted Linux host metrics.
type HostMetrics struct {
	Hostname      string        `json:"hostname"`
	UptimeSeconds uint64        `json:"uptime_seconds"`
	Memory        MemoryMetrics `json:"memory"`
	Load          LoadMetrics   `json:"load"`
	Disks         []DiskMetric  `json:"disks"`
}

// Reader collects host metrics without touching Docker.
type Reader struct {
	procRoot   string
	statfsFunc func(path string) (total, free, avail uint64, err error)
}

// NewReader creates a host metrics reader using /proc and OS statfs.
func NewReader() *Reader {
	return &Reader{
		procRoot:   "/proc",
		statfsFunc: readDiskBytes,
	}
}

// Collect reads hostname, uptime, load, memory, and configured disk mounts.
func (r *Reader) Collect(hostLabel string, mounts []config.MountConfig) HostMetrics {
	hostname := hostLabel
	if hostname == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			hostname = h
		} else {
			hostname = "home-server"
		}
	}

	m := HostMetrics{
		Hostname: hostname,
		Disks:    make([]DiskMetric, 0, len(mounts)),
	}

	if data, err := os.ReadFile(r.procRoot + "/uptime"); err == nil {
		m.UptimeSeconds = parseUptime(string(data))
	}
	if data, err := os.ReadFile(r.procRoot + "/loadavg"); err == nil {
		m.Load = parseLoadAvg(string(data))
	}
	if data, err := os.ReadFile(r.procRoot + "/meminfo"); err == nil {
		m.Memory = parseMemInfo(string(data))
	}

	for _, mount := range mounts {
		dm := DiskMetric{
			ID:    mount.ID,
			Label: mount.Label,
			Path:  mount.Path,
		}
		total, free, avail, err := r.statfsFunc(mount.Path)
		if err != nil {
			dm.Error = err.Error()
		} else {
			dm.Available = true
			dm.TotalBytes = total
			dm.FreeBytes = free
			dm.AvailableBytes = avail
			if total >= free {
				dm.UsedBytes = total - free
			}
			if total > 0 {
				dm.UsedPercent = float64(dm.UsedBytes) / float64(total) * 100
			}
		}
		m.Disks = append(m.Disks, dm)
	}

	return m
}

func parseUptime(raw string) uint64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return uint64(v)
}

func parseLoadAvg(raw string) LoadMetrics {
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return LoadMetrics{}
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return LoadMetrics{Load1: l1, Load5: l5, Load15: l15}
}

func parseMemInfo(raw string) MemoryMetrics {
	vals := make(map[string]uint64)
	for _, line := range strings.Split(raw, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		vals[key] = kb * 1024
	}

	total := vals["MemTotal"]
	avail, hasAvail := vals["MemAvailable"]
	if !hasAvail {
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	if avail > total {
		avail = total
	}
	used := total - avail
	var pct float64
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return MemoryMetrics{
		TotalBytes:     total,
		UsedBytes:      used,
		AvailableBytes: avail,
		UsedPercent:    pct,
	}
}

func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
