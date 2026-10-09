package config

import (
	"strings"
	"testing"
	"time"
)

var envKeys = []string{
	"EY_DISK_THRESHOLD", "EY_MEM_THRESHOLD", "EY_CPU_THRESHOLD",
	"EY_PATH", "EY_WEBHOOK", "EY_FORMAT", "EY_DAEMON", "EY_INTERVAL",
	"EY_ALERT_MODE", "EY_COOLDOWN", "EY_TIMEOUT", "EY_JSON", "EY_EXIT_CODE",
}

// clearEnv blanks every EY_* variable so tests are independent of the host.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
}

func TestParseDefaults(t *testing.T) {
	clearEnv(t)
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
	clearEnv(t)
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
	clearEnv(t)
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
	clearEnv(t)
	if _, err := Parse([]string{"-webhook", "https://discord.com/api/webhooks/1/token"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func TestEnvDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("EY_DISK_THRESHOLD", "42.5")
	t.Setenv("EY_WEBHOOK", "https://hooks.slack.com/services/env")
	t.Setenv("EY_FORMAT", "slack")
	t.Setenv("EY_DAEMON", "true")
	t.Setenv("EY_INTERVAL", "15s")
	t.Setenv("EY_ALERT_MODE", "cooldown")
	t.Setenv("EY_EXIT_CODE", "1")

	c, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DiskThreshold != 42.5 {
		t.Errorf("DiskThreshold = %g, want 42.5", c.DiskThreshold)
	}
	if c.Webhook != "https://hooks.slack.com/services/env" || c.Format != FormatSlack {
		t.Errorf("Webhook/Format = %q/%q", c.Webhook, c.Format)
	}
	if !c.Daemon || c.Interval != 15*time.Second {
		t.Errorf("Daemon/Interval = %v/%s", c.Daemon, c.Interval)
	}
	if c.AlertMode != AlertCooldown {
		t.Errorf("AlertMode = %q, want cooldown", c.AlertMode)
	}
	if !c.ExitCode {
		t.Error("ExitCode = false, want true")
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("EY_DISK_THRESHOLD", "42.5")
	t.Setenv("EY_INTERVAL", "15s")

	c, err := Parse([]string{"-disk-threshold", "70", "-interval", "5s"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DiskThreshold != 70 {
		t.Errorf("DiskThreshold = %g, want flag value 70", c.DiskThreshold)
	}
	if c.Interval != 5*time.Second {
		t.Errorf("Interval = %s, want flag value 5s", c.Interval)
	}
}

func TestInvalidEnvWithNoFlagErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("EY_INTERVAL", "banana")

	_, err := Parse(nil)
	if err == nil {
		t.Fatal("Parse = nil error, want error for invalid EY_INTERVAL")
	}
	if !strings.Contains(err.Error(), "EY_INTERVAL") {
		t.Errorf("error %q should mention EY_INTERVAL", err)
	}
}

func TestInvalidEnvIgnoredWhenFlagSet(t *testing.T) {
	clearEnv(t)
	t.Setenv("EY_INTERVAL", "banana")

	if _, err := Parse([]string{"-interval", "5s"}); err != nil {
		t.Fatalf("Parse: %v, invalid env must be ignored when flag is set", err)
	}
}
