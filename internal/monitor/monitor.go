package monitor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
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

	// Process fields are populated only when the process metric is selected.
	ProcessCount      int           `json:"process_count,omitempty"`
	ProcessTopPercent float64       `json:"process_top_percent,omitempty"`
	Processes         []ProcessInfo `json:"processes,omitempty"`
}

// ProcessInfo describes a single process in the top-consumer list.
type ProcessInfo struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name"`
	CPUPercent float64 `json:"cpu_percent"`
	RSSBytes   uint64  `json:"rss_bytes"`
	MemPercent float64 `json:"mem_percent"`
}

// SortKey selects the dimension used to rank processes.
type SortKey string

const (
	SortCPU SortKey = "cpu"
	SortMem SortKey = "memory"
)

// Breach describes a single metric that exceeded its threshold.
type Breach struct {
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

// Selection reports which metrics are enabled. A disabled metric is not
// collected, displayed, or evaluated for alerts.
type Selection struct {
	Disk    bool
	Mem     bool
	CPU     bool
	Process bool
}

// AllSelection enables every metric.
func AllSelection() Selection { return Selection{Disk: true, Mem: true, CPU: true, Process: true} }

// Any reports whether at least one metric is selected.
func (s Selection) Any() bool { return s.Disk || s.Mem || s.CPU || s.Process }

// Has reports whether the named metric is selected.
func (s Selection) Has(metric string) bool {
	switch metric {
	case "disk":
		return s.Disk
	case "memory":
		return s.Mem
	case "cpu":
		return s.CPU
	case "process":
		return s.Process
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
		case "process":
			s.Process = true
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

	// Process is the percentage threshold applied to the top consumer's value
	// in the configured sort dimension. ProcessCount, when greater than zero,
	// is the maximum number of processes allowed.
	Process      float64
	ProcessCount int
}

// PrimeCPU seeds gopsutil's package-level CPU times so that the first
// subsequent cpu.Percent(0, ...) call measures the interval since priming
// instead of returning a zero value. Call it once before a daemon loop.
func PrimeCPU() {
	_, _ = cpu.Percent(0, false)
}

// TopProcesses returns up to limit processes ranked by sortKey, along with the
// total number of processes seen. CPU usage is measured over interval: the
// per-process counters are primed, then read after the interval elapses.
func TopProcesses(limit int, sortKey SortKey, interval time.Duration) ([]ProcessInfo, int, error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, 0, fmt.Errorf("list processes: %w", err)
	}

	for _, p := range procs {
		_, _ = p.Percent(0)
	}
	if interval > 0 {
		time.Sleep(interval)
	}

	infos := make([]ProcessInfo, 0, len(procs))
	for _, p := range procs {
		pct, err := p.Percent(0)
		if err != nil {
			continue
		}
		var rss uint64
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = mi.RSS
		}
		memPct, err := p.MemoryPercent()
		if err != nil {
			memPct = 0
		}
		name, err := p.Name()
		if err != nil || name == "" {
			name = "?"
		}
		infos = append(infos, ProcessInfo{
			PID:        p.Pid,
			Name:       name,
			CPUPercent: pct,
			RSSBytes:   rss,
			MemPercent: float64(memPct),
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		a, b := infos[i], infos[j]
		if sortKey == SortMem {
			if a.MemPercent != b.MemPercent {
				return a.MemPercent > b.MemPercent
			}
			return a.CPUPercent > b.CPUPercent
		}
		if a.CPUPercent != b.CPUPercent {
			return a.CPUPercent > b.CPUPercent
		}
		return a.MemPercent > b.MemPercent
	})

	total := len(infos)
	if limit > 0 && len(infos) > limit {
		infos = infos[:limit]
	}
	return infos, total, nil
}

// Value returns the usage percentage of p in the given sort dimension.
func (p ProcessInfo) Value(key SortKey) float64 {
	if key == SortMem {
		return p.MemPercent
	}
	return p.CPUPercent
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
	if sel.Process {
		if s.ProcessTopPercent > t.Process {
			breaches = append(breaches, Breach{Metric: "process", Value: s.ProcessTopPercent, Threshold: t.Process})
		}
		if t.ProcessCount > 0 && s.ProcessCount > t.ProcessCount {
			breaches = append(breaches, Breach{Metric: "process_count", Value: float64(s.ProcessCount), Threshold: float64(t.ProcessCount)})
		}
	}
	return breaches
}

// GiB converts bytes to gibibytes.
func GiB(bytes uint64) float64 {
	return float64(bytes) / (1 << 30)
}
