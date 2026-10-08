package monitor

import (
	"fmt"
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

// Collect samples disk, memory, and CPU usage for the given path.
// cpuInterval is how long to sample CPU utilization; pass 0 in daemon mode
// to measure usage since the previous call.
func Collect(path string, cpuInterval time.Duration) (Snapshot, error) {
	s := Snapshot{Timestamp: time.Now()}

	diskUsage, err := disk.Usage(path)
	if err != nil {
		return s, fmt.Errorf("disk usage for %s: %w", path, err)
	}
	s.DiskUsedPercent = diskUsage.UsedPercent
	s.DiskTotalBytes = diskUsage.Total
	s.DiskUsedBytes = diskUsage.Used

	vm, err := mem.VirtualMemory()
	if err != nil {
		return s, fmt.Errorf("virtual memory: %w", err)
	}
	s.MemUsedPercent = vm.UsedPercent
	s.MemTotalBytes = vm.Total
	s.MemUsedBytes = vm.Used

	percent, err := cpu.Percent(cpuInterval, false)
	if err != nil {
		return s, fmt.Errorf("cpu percent: %w", err)
	}
	if len(percent) > 0 {
		s.CPUUsedPercent = percent[0]
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

// Evaluate returns one Breach per metric whose value exceeds its threshold.
func Evaluate(s Snapshot, t Thresholds) []Breach {
	var breaches []Breach
	if s.DiskUsedPercent > t.Disk {
		breaches = append(breaches, Breach{Metric: "disk", Value: s.DiskUsedPercent, Threshold: t.Disk})
	}
	if s.MemUsedPercent > t.Mem {
		breaches = append(breaches, Breach{Metric: "memory", Value: s.MemUsedPercent, Threshold: t.Mem})
	}
	if s.CPUUsedPercent > t.CPU {
		breaches = append(breaches, Breach{Metric: "cpu", Value: s.CPUUsedPercent, Threshold: t.CPU})
	}
	return breaches
}

// GiB converts bytes to gibibytes.
func GiB(bytes uint64) float64 {
	return float64(bytes) / (1 << 30)
}
