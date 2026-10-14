package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"ey/internal/alerter"
	"ey/internal/config"
	"ey/internal/monitor"
	"ey/internal/state"
)

var (
	breach = []monitor.Breach{{Metric: "disk", Value: 95, Threshold: 80}}
	clear  []monitor.Breach
	t0     = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPolicyAlways(t *testing.T) {
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertAlways, Cooldown: time.Hour}, nil, testLogger())
	for i := range 3 {
		if !p.shouldSend(breach, t0.Add(time.Duration(i)*time.Minute)) {
			t.Errorf("cycle %d: should send in always mode", i)
		}
	}
	if p.shouldSend(clear, t0) {
		t.Error("no breaches must not send")
	}
}

func TestPolicyOnce(t *testing.T) {
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertOnce, Cooldown: time.Hour}, nil, testLogger())

	if !p.shouldSend(breach, t0) {
		t.Error("first breach must send")
	}
	if p.shouldSend(breach, t0.Add(time.Minute)) {
		t.Error("second breach must not send while still breached")
	}
	if p.shouldSend(clear, t0.Add(2*time.Minute)) {
		t.Error("recovery must not send")
	}
	if !p.shouldSend(breach, t0.Add(3*time.Minute)) {
		t.Error("breach after recovery must re-arm and send")
	}
}

func TestPolicyCooldown(t *testing.T) {
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertCooldown, Cooldown: 10 * time.Minute}, nil, testLogger())

	if !p.shouldSend(breach, t0) {
		t.Error("first breach must send")
	}
	if p.shouldSend(breach, t0.Add(5*time.Minute)) {
		t.Error("breach inside cooldown must not send")
	}
	if p.shouldSend(clear, t0.Add(6*time.Minute)) {
		t.Error("recovery must not send")
	}
	if !p.shouldSend(breach, t0.Add(11*time.Minute)) {
		t.Error("breach after cooldown must send")
	}
}

func TestPolicyCooldownRequiresPriorSend(t *testing.T) {
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertCooldown, Cooldown: 10 * time.Minute}, nil, testLogger())
	if !p.shouldSend(breach, t0) {
		t.Error("zero lastSent must allow send")
	}
}

func TestPolicyStatePersistsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	cfg := &config.Config{AlertMode: config.AlertOnce, Cooldown: time.Hour}

	p := newAlertPolicy(cfg, state.Open(path, testLogger()), testLogger())
	if !p.shouldSend(breach, t0) {
		t.Fatal("first breach must send")
	}
	p.flush()

	// A fresh process must not re-send while still breached.
	restarted := newAlertPolicy(cfg, state.Open(path, testLogger()), testLogger())
	if restarted.shouldSend(breach, t0.Add(time.Minute)) {
		t.Error("once-mode armed=false must survive a restart")
	}
}

func TestPolicyStatePersistsCooldown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	cfg := &config.Config{AlertMode: config.AlertCooldown, Cooldown: 10 * time.Minute}

	p := newAlertPolicy(cfg, state.Open(path, testLogger()), testLogger())
	if !p.shouldSend(breach, t0) {
		t.Fatal("first breach must send")
	}
	p.flush()

	restarted := newAlertPolicy(cfg, state.Open(path, testLogger()), testLogger())
	if restarted.shouldSend(breach, t0.Add(5*time.Minute)) {
		t.Error("cooldown must survive a restart")
	}
	if !restarted.shouldSend(breach, t0.Add(11*time.Minute)) {
		t.Error("breach after cooldown must send post-restart")
	}
}

func TestNextDelayOverrun(t *testing.T) {
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertAlways}, nil, testLogger())
	if d := p.nextDelay(time.Minute, time.Now()); d <= 0 || d > time.Minute {
		t.Errorf("nextDelay = %s, want within (0,1m]", d)
	}
	if d := p.nextDelay(time.Millisecond, time.Now().Add(-time.Hour)); d != 0 {
		t.Errorf("overran nextDelay = %s, want 0", d)
	}
}

// fakeSink records the calls a metricsSink receives.
type fakeSink struct {
	observed int
	errors   int
	last     monitor.Snapshot
}

func (f *fakeSink) Observe(s monitor.Snapshot, _ []monitor.Breach) {
	f.observed++
	f.last = s
}

func (f *fakeSink) ObserveError(error) { f.errors++ }

func newTestClient() *alerter.Client {
	return alerter.New("", alerter.Options{Logger: testLogger()})
}

func TestStepFeedsSinkOnSuccess(t *testing.T) {
	cfg := &config.Config{Path: t.TempDir(), Quiet: true, AlertMode: config.AlertAlways}
	p := newAlertPolicy(cfg, nil, testLogger())
	sink := &fakeSink{}
	p.sink = sink

	code := p.step(context.Background(), newTestClient(),
		monitor.Thresholds{Disk: 100}, cfg, monitor.Selection{Disk: true}, 0, false)

	if code != 0 {
		t.Errorf("step code = %d, want 0", code)
	}
	if sink.observed != 1 || sink.errors != 0 {
		t.Errorf("sink observed=%d errors=%d, want 1/0", sink.observed, sink.errors)
	}
	if sink.last.DiskTotalBytes == 0 {
		t.Error("sink did not receive a populated snapshot")
	}
}

func TestStepReportsCollectErrorToSink(t *testing.T) {
	cfg := &config.Config{Path: filepath.Join(t.TempDir(), "does", "not", "exist"), Quiet: true}
	p := newAlertPolicy(cfg, nil, testLogger())
	sink := &fakeSink{}
	p.sink = sink

	code := p.step(context.Background(), newTestClient(),
		monitor.Thresholds{}, cfg, monitor.Selection{Disk: true}, 0, false)

	if code != 2 {
		t.Errorf("step code = %d, want 2 on collect error", code)
	}
	if sink.errors != 1 || sink.observed != 0 {
		t.Errorf("sink errors=%d observed=%d, want 1/0", sink.errors, sink.observed)
	}
}

func TestRunPrometheusRequiresDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("EY_CONFIG", "")
	if code := run([]string{"-prometheus-listen", ":9101", "-path", "/"}); code != 2 {
		t.Errorf("run without -daemon = %d, want 2", code)
	}
}
