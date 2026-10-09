package monitor

import (
	"testing"
	"time"
)

func TestEvaluate(t *testing.T) {
	th := Thresholds{Disk: 80, Mem: 90, CPU: 90}

	tests := []struct {
		name string
		snap Snapshot
		want []string
	}{
		{"all ok", Snapshot{DiskUsedPercent: 50, MemUsedPercent: 60, CPUUsedPercent: 10}, nil},
		{"exact threshold not a breach", Snapshot{DiskUsedPercent: 80, MemUsedPercent: 90, CPUUsedPercent: 90}, nil},
		{"disk only", Snapshot{DiskUsedPercent: 81, MemUsedPercent: 60, CPUUsedPercent: 10}, []string{"disk"}},
		{"all breached", Snapshot{DiskUsedPercent: 99, MemUsedPercent: 95, CPUUsedPercent: 91}, []string{"disk", "memory", "cpu"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			breaches := Evaluate(tt.snap, th, AllSelection())
			if len(breaches) != len(tt.want) {
				t.Fatalf("got %d breaches (%v), want %d", len(breaches), breaches, len(tt.want))
			}
			for i, b := range breaches {
				if b.Metric != tt.want[i] {
					t.Errorf("breach[%d].Metric = %q, want %q", i, b.Metric, tt.want[i])
				}
				if b.Value <= b.Threshold {
					t.Errorf("breach[%d] value %g not above threshold %g", i, b.Value, b.Threshold)
				}
			}
		})
	}
}

func TestEvaluateRespectsSelection(t *testing.T) {
	th := Thresholds{Disk: 80, Mem: 90, CPU: 90}
	snap := Snapshot{DiskUsedPercent: 99, MemUsedPercent: 99, CPUUsedPercent: 99}

	breaches := Evaluate(snap, th, Selection{Mem: true})
	if len(breaches) != 1 || breaches[0].Metric != "memory" {
		t.Fatalf("mem-only selection got %v, want [memory]", breaches)
	}

	if got := Evaluate(snap, th, Selection{}); len(got) != 0 {
		t.Fatalf("empty selection got %v, want no breaches", got)
	}
}

func TestParseSelection(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Selection
	}{
		{"disk,memory,cpu", AllSelection()},
		{"cpu,disk", Selection{Disk: true, CPU: true}},
		{"memory memory", Selection{Mem: true}},
		{" DISK ,cpu ", Selection{Disk: true, CPU: true}},
	} {
		got, err := ParseSelection(tt.in)
		if err != nil {
			t.Errorf("ParseSelection(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseSelection(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}

	for _, in := range []string{"", "network", "disk,wat"} {
		if _, err := ParseSelection(in); err == nil {
			t.Errorf("ParseSelection(%q) = nil error, want error", in)
		}
	}
}

func TestEvaluateCarriesValues(t *testing.T) {
	breaches := Evaluate(
		Snapshot{DiskUsedPercent: 95.5, MemUsedPercent: 99, CPUUsedPercent: 99},
		Thresholds{Disk: 80, Mem: 100, CPU: 100},
		AllSelection(),
	)
	if len(breaches) != 1 {
		t.Fatalf("got %d breaches, want 1", len(breaches))
	}
	b := breaches[0]
	if b.Metric != "disk" || b.Value != 95.5 || b.Threshold != 80 {
		t.Errorf("breach = %+v, want {disk 95.5 80}", b)
	}
}

func TestGiB(t *testing.T) {
	if got := GiB(1 << 30); got != 1 {
		t.Errorf("GiB(1GiB) = %g, want 1", got)
	}
	if got := GiB(0); got != 0 {
		t.Errorf("GiB(0) = %g, want 0", got)
	}
}

// TestCollect is a smoke test that runs against the host system.
func TestCollect(t *testing.T) {
	s, err := Collect("/", 100*time.Millisecond, AllSelection())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if s.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
	for name, v := range map[string]float64{
		"disk": s.DiskUsedPercent,
		"mem":  s.MemUsedPercent,
		"cpu":  s.CPUUsedPercent,
	} {
		if v < 0 || v > 100 {
			t.Errorf("%s percent = %g, want 0..100", name, v)
		}
	}
	if s.DiskTotalBytes == 0 || s.MemTotalBytes == 0 {
		t.Errorf("total bytes = disk %d, mem %d; want non-zero", s.DiskTotalBytes, s.MemTotalBytes)
	}
	if s.DiskUsedBytes > s.DiskTotalBytes || s.MemUsedBytes > s.MemTotalBytes {
		t.Error("used bytes exceed total bytes")
	}
}
