package main

import (
	"testing"
	"time"

	"ey/internal/config"
	"ey/internal/monitor"
)

var (
	breach = []monitor.Breach{{Metric: "disk", Value: 95, Threshold: 80}}
	clear  []monitor.Breach
	t0     = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

func TestPolicyAlways(t *testing.T) {
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertAlways, Cooldown: time.Hour})
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
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertOnce, Cooldown: time.Hour})

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
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertCooldown, Cooldown: 10 * time.Minute})

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
	p := newAlertPolicy(&config.Config{AlertMode: config.AlertCooldown, Cooldown: 10 * time.Minute})
	if !p.shouldSend(breach, t0) {
		t.Error("zero lastSent must allow send")
	}
}
