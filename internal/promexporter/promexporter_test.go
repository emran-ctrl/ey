package promexporter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"ey/internal/monitor"
)

// families returns the set of metric family names currently gathered.
func families(t *testing.T, e *Exporter) map[string]bool {
	t.Helper()
	mfs, err := e.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	got := make(map[string]bool, len(mfs))
	for _, mf := range mfs {
		got[mf.GetName()] = true
	}
	return got
}

func TestNewRegistersOnlySelected(t *testing.T) {
	diskOnly := families(t, New(Options{Selection: monitor.Selection{Disk: true}}))

	for _, want := range []string{
		"ey_build_info", "ey_threshold_percent", "ey_threshold_breached",
		"ey_last_collect_timestamp_seconds", "ey_collect_errors_total",
		"ey_disk_used_percent", "ey_disk_total_bytes", "ey_disk_used_bytes",
	} {
		if !diskOnly[want] {
			t.Errorf("disk-only exporter missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"ey_memory_used_percent", "ey_cpu_used_percent",
	} {
		if diskOnly[unwanted] {
			t.Errorf("disk-only exporter unexpectedly exposed %q", unwanted)
		}
	}

	all := families(t, New(Options{Selection: monitor.AllSelection()}))
	for _, want := range []string{
		"ey_disk_used_percent", "ey_memory_used_percent", "ey_memory_used_bytes",
		"ey_cpu_used_percent",
	} {
		if !all[want] {
			t.Errorf("all-metrics exporter missing %q", want)
		}
	}
}

func TestObserveValues(t *testing.T) {
	e := New(Options{
		Selection:  monitor.AllSelection(),
		Thresholds: monitor.Thresholds{Disk: 80, Mem: 90, CPU: 95},
		Version:    "test",
	})
	e.Observe(monitor.Snapshot{
		Timestamp:       time.Unix(1700000000, 0),
		DiskUsedPercent: 42.5, DiskTotalBytes: 1000, DiskUsedBytes: 425,
		MemUsedPercent: 71.25, MemTotalBytes: 2000, MemUsedBytes: 1425,
		CPUUsedPercent: 12.5,
	}, nil)

	checks := map[string]struct {
		gauge prometheus.Collector
		want  float64
	}{
		"disk_used_percent":   {e.diskUsedPercent, 42.5},
		"disk_total_bytes":    {e.diskTotalBytes, 1000},
		"disk_used_bytes":     {e.diskUsedBytes, 425},
		"memory_used_percent": {e.memUsedPercent, 71.25},
		"memory_total_bytes":  {e.memTotalBytes, 2000},
		"memory_used_bytes":   {e.memUsedBytes, 1425},
		"cpu_used_percent":    {e.cpuUsedPercent, 12.5},
		"last_collect":        {e.lastCollect, 1700000000},
	}
	for name, c := range checks {
		if got := testutil.ToFloat64(c.gauge); got != c.want {
			t.Errorf("%s = %g, want %g", name, got, c.want)
		}
	}

	if got := testutil.ToFloat64(e.thresholdPercent.WithLabelValues("disk")); got != 80 {
		t.Errorf("threshold_percent{disk} = %g, want 80", got)
	}
}

func TestObserveBreachResets(t *testing.T) {
	e := New(Options{
		Selection:  monitor.Selection{Disk: true, Mem: true},
		Thresholds: monitor.Thresholds{Disk: 80, Mem: 90},
	})
	snap := monitor.Snapshot{DiskUsedPercent: 99}

	e.Observe(snap, []monitor.Breach{{Metric: "disk", Value: 99, Threshold: 80}})
	if got := testutil.ToFloat64(e.thresholdBreached.WithLabelValues("disk")); got != 1 {
		t.Errorf("breached{disk} = %g after breach, want 1", got)
	}

	e.Observe(snap, nil)
	if got := testutil.ToFloat64(e.thresholdBreached.WithLabelValues("disk")); got != 0 {
		t.Errorf("breached{disk} = %g after clear, want 0", got)
	}
	if got := testutil.ToFloat64(e.thresholdBreached.WithLabelValues("memory")); got != 0 {
		t.Errorf("breached{memory} = %g, want 0", got)
	}
}

func TestObserveErrorCounts(t *testing.T) {
	e := New(Options{Selection: monitor.AllSelection()})
	e.ObserveError(errors.New("boom"))
	e.ObserveError(errors.New("boom"))
	if got := testutil.ToFloat64(e.collectErrors); got != 2 {
		t.Errorf("collect_errors_total = %g, want 2", got)
	}
}

func TestHandlerRoutes(t *testing.T) {
	e := New(Options{
		Selection: monitor.Selection{Disk: true},
		Version:   "v9",
	})
	e.Observe(monitor.Snapshot{DiskUsedPercent: 3}, nil)

	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"ey_disk_used_percent", "ey_build_info"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics output missing %q", want)
		}
	}

	resp2, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET / status = %d, want 404", resp2.StatusCode)
	}
}

func TestServeLifecycle(t *testing.T) {
	e := New(Options{Selection: monitor.Selection{CPU: true}})

	if _, err := Serve(context.Background(), "127.0.0.1:-1", e.Handler(), nil); err == nil {
		t.Fatal("Serve with bad address = nil error, want error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Serve(ctx, "127.0.0.1:0", e.Handler(), nil)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer s.Close(context.Background())

	resp, err := http.Get("http://" + s.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("GET %s/metrics: %v", s.Addr(), err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
