package monitor

import (
	"fmt"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

// Snapshot is the set of system metrics collected in a single pass.
type Snapshot struct {
	Timestamp       time.Time `json:"timestamp"`
	DiskUsedPercent float64   `json:"disk_used_percent"`
	DiskTotalBytes  uint64    `json:"disk_total_bytes"`
	DiskUsedBytes   uint64    `json:"disk_used_bytes"`
	MemUsedPercent  float64   `json:"mem_used_percent"`
	MemTotalBytes   uint64    `json:"mem_total_bytes"`
	MemUsedBytes    uint64    `json:"mem_used_bytes"`
	CPUUsedPercent  float64   `json:"cpu_used_percent"`
}

// Breach describes a single metric that exceeded its threshold.
type Breach struct {
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

// Selection reports which metrics are enabled. A disabled metric is not
// collected, displayed, or evaluated for alerts.
type Selection struct {
	Disk bool
	Mem  bool
	CPU  bool
}

// AllSelection enables every metric.
func AllSelection() Selection { return Selection{Disk: true, Mem: true, CPU: true} }

// Any reports whether at least one metric is selected.
func (s Selection) Any() bool { return s.Disk || s.Mem || s.CPU }

// Has reports whether the named metric is selected.
func (s Selection) Has(metric string) bool {
	switch metric {
	case "disk":
		return s.Disk
	case "memory":
		return s.Mem
	case "cpu":
		return s.CPU
	}
	return false
}

// ParseSelection parses a comma- or whitespace-separated metric list such as
// "disk,memory" into a Selection.
func ParseSelection(list string) (Selection, error) {
	var s Selection
	for _, raw := range strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "disk":
			s.Disk = true
		case "memory":
			s.Mem = true
		case "cpu":
			s.CPU = true
		default:
			return s, fmt.Errorf("unknown metric %q", raw)
		}
	}
	if !s.Any() {
		return s, fmt.Errorf("no metrics selected")
	}
	return s, nil
}

// Collect samples the selected disk, memory, and CPU usage.
// cpuInterval is how long to sample CPU utilization; pass 0 in daemon mode
// to measure usage since the previous call.
func Collect(path string, cpuInterval time.Duration, sel Selection) (Snapshot, error) {
	s := Snapshot{Timestamp: time.Now()}

	if sel.Disk {
		diskUsage, err := disk.Usage(path)
		if err != nil {
			return s, fmt.Errorf("disk usage for %s: %w", path, err)
		}
		s.DiskUsedPercent = diskUsage.UsedPercent
		s.DiskTotalBytes = diskUsage.Total
		s.DiskUsedBytes = diskUsage.Used
	}

	if sel.Mem {
		vm, err := mem.VirtualMemory()
		if err != nil {
			return s, fmt.Errorf("virtual memory: %w", err)
		}
		s.MemUsedPercent = vm.UsedPercent
		s.MemTotalBytes = vm.Total
		s.MemUsedBytes = vm.Used
	}

	if sel.CPU {
		percent, err := cpu.Percent(cpuInterval, false)
		if err != nil {
			return s, fmt.Errorf("cpu percent: %w", err)
		}
		if len(percent) > 0 {
			s.CPUUsedPercent = percent[0]
		}
	}

	return s, nil
}

// Thresholds holds the per-metric limits to evaluate against.
type Thresholds struct {
	Disk float64
	Mem  float64
	CPU  float64
}

// PrimeCPU seeds gopsutil's package-level CPU times so that the first
// subsequent cpu.Percent(0, ...) call measures the interval since priming
// instead of returning a zero value. Call it once before a daemon loop.
func PrimeCPU() {
	_, _ = cpu.Percent(0, false)
}

// Evaluate returns one Breach per selected metric whose value exceeds its
// threshold.
func Evaluate(s Snapshot, t Thresholds, sel Selection) []Breach {
	var breaches []Breach
	if sel.Disk && s.DiskUsedPercent > t.Disk {
		breaches = append(breaches, Breach{Metric: "disk", Value: s.DiskUsedPercent, Threshold: t.Disk})
	}
	if sel.Mem && s.MemUsedPercent > t.Mem {
		breaches = append(breaches, Breach{Metric: "memory", Value: s.MemUsedPercent, Threshold: t.Mem})
	}
	if sel.CPU && s.CPUUsedPercent > t.CPU {
		breaches = append(breaches, Breach{Metric: "cpu", Value: s.CPUUsedPercent, Threshold: t.CPU})
	}
	return breaches
}

// GiB converts bytes to gibibytes.
func GiB(bytes uint64) float64 {
	return float64(bytes) / (1 << 30)
}
