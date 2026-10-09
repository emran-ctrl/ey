package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

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
