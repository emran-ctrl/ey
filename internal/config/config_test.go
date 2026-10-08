package config

import (
	"testing"
	"time"
)

func TestParseDefaults(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DiskThreshold != 80 || c.MemThreshold != 90 || c.CPUThreshold != 90 {
		t.Errorf("thresholds = %g/%g/%g, want 80/90/90", c.DiskThreshold, c.MemThreshold, c.CPUThreshold)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if c.Format != FormatAuto || c.AlertMode != AlertAlways {
		t.Errorf("Format/AlertMode = %q/%q, want auto/always", c.Format, c.AlertMode)
	}
	if c.Daemon {
		t.Error("Daemon = true, want false")
	}
	if c.Interval != 60*time.Second || c.Cooldown != 10*time.Minute || c.Timeout != 5*time.Second {
		t.Errorf("Interval/Cooldown/Timeout = %s/%s/%s", c.Interval, c.Cooldown, c.Timeout)
	}
	if c.JSON || c.ExitCode {
		t.Error("JSON/ExitCode should default to false")
	}
}

func TestParseOverrides(t *testing.T) {
	c, err := Parse([]string{
		"-disk-threshold", "55.5",
		"-mem-threshold", "70",
		"-cpu-threshold", "60",
		"-path", "/var",
		"-webhook", "https://hooks.slack.com/services/abc",
		"-format", "slack",
		"-daemon",
		"-interval", "30s",
		"-alert-mode", "COOLDOWN",
		"-cooldown", "5m",
		"-timeout", "2s",
		"-json",
		"-exit-code",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DiskThreshold != 55.5 || c.MemThreshold != 70 || c.CPUThreshold != 60 {
		t.Errorf("thresholds = %g/%g/%g", c.DiskThreshold, c.MemThreshold, c.CPUThreshold)
	}
	if c.Path != "/var" || !c.Daemon || !c.JSON || !c.ExitCode {
		t.Errorf("path/daemon/json/exit = %q/%v/%v/%v", c.Path, c.Daemon, c.JSON, c.ExitCode)
	}
	if c.Interval != 30*time.Second || c.Cooldown != 5*time.Minute || c.Timeout != 2*time.Second {
		t.Errorf("durations = %s/%s/%s", c.Interval, c.Cooldown, c.Timeout)
	}
	if c.AlertMode != AlertCooldown {
		t.Errorf("AlertMode = %q, want cooldown (case-normalized)", c.AlertMode)
	}
	if c.Format != FormatSlack {
		t.Errorf("Format = %q, want slack", c.Format)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"threshold too high", []string{"-disk-threshold", "101"}},
		{"threshold negative", []string{"-mem-threshold", "-1"}},
		{"bad format", []string{"-format", "pagerduty"}},
		{"bad alert mode", []string{"-alert-mode", "sometimes"}},
		{"bad interval", []string{"-interval", "0s"}},
		{"bad cooldown", []string{"-cooldown", "0s"}},
		{"bad timeout", []string{"-timeout", "-5s"}},
		{"empty path", []string{"-path", ""}},
		{"bad webhook scheme", []string{"-webhook", "ftp://example.com/hook"}},
		{"unparseable webhook", []string{"-webhook", "://bad"}},
		{"unknown flag", []string{"-nope"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.args); err == nil {
				t.Errorf("Parse(%v) = nil error, want error", tt.args)
			}
		})
	}
}

func TestParseAcceptsValidWebhook(t *testing.T) {
	if _, err := Parse([]string{"-webhook", "https://discord.com/api/webhooks/1/token"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}
