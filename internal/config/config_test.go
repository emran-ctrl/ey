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
	"EY_LOG_FORMAT", "EY_LOG_LEVEL", "EY_QUIET", "EY_RETRIES",
	"EY_RETRY_BACKOFF", "EY_STATE_FILE", "EY_NO_STATE",
	"EY_METRICS", "EY_COLOR", "EY_CONFIG",
	"EY_PROCESS_COUNT", "EY_PROCESS_SORT", "EY_PROCESS_THRESHOLD", "EY_PROCESS_COUNT_THRESHOLD",
	"EY_PROMETHEUS_LISTEN",
}

// clearEnv blanks every EY_* variable and points the config directory at an
// empty temp dir so tests are independent of the host.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	if c.LogFormat != LogFormatText || c.LogLevel != LogLevelInfo || c.Quiet {
		t.Errorf("LogFormat/LogLevel/Quiet = %q/%q/%v, want text/info/false", c.LogFormat, c.LogLevel, c.Quiet)
	}
	if c.Retries != 3 || c.RetryBackoff != 500*time.Millisecond {
		t.Errorf("Retries/RetryBackoff = %d/%s, want 3/500ms", c.Retries, c.RetryBackoff)
	}
	if c.StateFile != "" || c.NoState {
		t.Errorf("StateFile/NoState = %q/%v, want empty/false", c.StateFile, c.NoState)
	}
	if c.Metrics != DefaultMetrics || c.Color != ColorAuto {
		t.Errorf("Metrics/Color = %q/%q, want %q/auto", c.Metrics, c.Color, DefaultMetrics)
	}
	if c.ProcessCount != 5 || c.ProcessSort != ProcessSortCPU || c.ProcessThreshold != 90 || c.ProcessCountThreshold != 0 {
		t.Errorf("process defaults = %d/%q/%g/%d, want 5/cpu/90/0",
			c.ProcessCount, c.ProcessSort, c.ProcessThreshold, c.ProcessCountThreshold)
	}
	if c.PrometheusListen != "" {
		t.Errorf("PrometheusListen = %q, want empty (disabled by default)", c.PrometheusListen)
	}
	if c.ConfigPath == "" {
		t.Error("ConfigPath = empty, want resolved path")
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
		{"bad log format", []string{"-log-format", "xml"}},
		{"bad log level", []string{"-log-level", "trace"}},
		{"negative retries", []string{"-retries", "-1"}},
		{"zero retry backoff", []string{"-retry-backoff", "0s"}},
		{"bad metrics", []string{"-metrics", "network"}},
		{"empty metrics", []string{"-metrics", ""}},
		{"bad color", []string{"-color", "rainbow"}},
		{"bad process sort", []string{"-process-sort", "disk"}},
		{"zero process count", []string{"-process-count", "0"}},
		{"process threshold high", []string{"-process-threshold", "101"}},
		{"negative process count threshold", []string{"-process-count-threshold", "-1"}},
		{"bad prometheus listen missing port", []string{"-prometheus-listen", "9101"}},
		{"bad prometheus listen port", []string{"-prometheus-listen", "127.0.0.1:99999"}},
		{"bad prometheus listen non-numeric port", []string{"-prometheus-listen", "host:http"}},
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

func TestProcessOptions(t *testing.T) {
	clearEnv(t)

	c, err := Parse([]string{
		"-process-count", "12",
		"-process-sort", "MEMORY",
		"-process-threshold", "75",
		"-process-count-threshold", "500",
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.ProcessCount != 12 || c.ProcessSort != ProcessSortMem || c.ProcessThreshold != 75 || c.ProcessCountThreshold != 500 {
		t.Errorf("process options = %d/%q/%g/%d, want 12/memory/75/500",
			c.ProcessCount, c.ProcessSort, c.ProcessThreshold, c.ProcessCountThreshold)
	}
}

func TestProcessEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("EY_PROCESS_COUNT", "8")
	t.Setenv("EY_PROCESS_SORT", "memory")
	t.Setenv("EY_PROCESS_THRESHOLD", "70")
	t.Setenv("EY_PROCESS_COUNT_THRESHOLD", "200")

	c, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.ProcessCount != 8 || c.ProcessSort != ProcessSortMem || c.ProcessThreshold != 70 || c.ProcessCountThreshold != 200 {
		t.Errorf("process env = %d/%q/%g/%d, want 8/memory/70/200",
			c.ProcessCount, c.ProcessSort, c.ProcessThreshold, c.ProcessCountThreshold)
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

func TestEnvRetriesAndLogging(t *testing.T) {
	clearEnv(t)
	t.Setenv("EY_RETRIES", "7")
	t.Setenv("EY_RETRY_BACKOFF", "2s")
	t.Setenv("EY_LOG_FORMAT", "json")
	t.Setenv("EY_LOG_LEVEL", "debug")
	t.Setenv("EY_QUIET", "true")
	t.Setenv("EY_NO_STATE", "1")
	t.Setenv("EY_STATE_FILE", "/tmp/ey-state.json")

	c, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Retries != 7 || c.RetryBackoff != 2*time.Second {
		t.Errorf("Retries/RetryBackoff = %d/%s, want 7/2s", c.Retries, c.RetryBackoff)
	}
	if c.LogFormat != LogFormatJSON || c.LogLevel != LogLevelDebug || !c.Quiet {
		t.Errorf("LogFormat/LogLevel/Quiet = %q/%q/%v", c.LogFormat, c.LogLevel, c.Quiet)
	}
	if !c.NoState || c.StateFile != "/tmp/ey-state.json" {
		t.Errorf("NoState/StateFile = %v/%q", c.NoState, c.StateFile)
	}
}

func TestMetricsAndColorFlagsAndEnv(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]string{"-metrics", "MEMORY,disk", "-color", "ALWAYS"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Metrics != "disk,memory" {
		t.Errorf("Metrics = %q, want disk,memory (normalized)", c.Metrics)
	}
	if c.Color != ColorAlways {
		t.Errorf("Color = %q, want always", c.Color)
	}

	clearEnv(t)
	t.Setenv("EY_METRICS", "cpu")
	t.Setenv("EY_COLOR", "never")
	c, err = Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Metrics != "cpu" || c.Color != ColorNever {
		t.Errorf("Metrics/Color = %q/%q, want cpu/never from env", c.Metrics, c.Color)
	}
}

func TestProcessMetricAutoEnable(t *testing.T) {
	hasProcess := func(metrics string) bool {
		for _, m := range strings.Split(metrics, ",") {
			if m == "process" {
				return true
			}
		}
		return false
	}

	for _, tt := range []struct {
		name      string
		envMetric string
		envProc   string
		args      []string
		want      bool
	}{
		{"flag process-count", "", "", []string{"-process-count", "8"}, true},
		{"flag process-sort", "", "", []string{"-process-sort", "memory"}, true},
		{"flag process-threshold", "", "", []string{"-process-threshold", "75"}, true},
		{"flag process-count-threshold", "", "", []string{"-process-count-threshold", "500"}, true},
		{"flag metrics without process wins", "", "", []string{"-metrics", "cpu", "-process-count", "8"}, false},
		{"flag metrics with process", "", "", []string{"-metrics", "cpu,process"}, true},
		{"env process-count", "", "8", nil, true},
		{"env process-count overridden by metrics flag", "", "8", []string{"-metrics", "cpu"}, false},
		{"flag process beats env metrics", "cpu", "", []string{"-process-count", "8"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			if tt.envMetric != "" {
				t.Setenv("EY_METRICS", tt.envMetric)
			}
			if tt.envProc != "" {
				t.Setenv("EY_PROCESS_COUNT", tt.envProc)
			}
			c, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := hasProcess(c.Metrics); got != tt.want {
				t.Errorf("Metrics = %q, hasProcess = %v, want %v", c.Metrics, got, tt.want)
			}
		})
	}
}

func TestPrometheusListenFlagAndEnv(t *testing.T) {
	clearEnv(t)
	c, err := Parse([]string{"-prometheus-listen", "127.0.0.1:9101"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.PrometheusListen != "127.0.0.1:9101" {
		t.Errorf("PrometheusListen = %q, want 127.0.0.1:9101", c.PrometheusListen)
	}

	clearEnv(t)
	t.Setenv("EY_PROMETHEUS_LISTEN", ":9101")
	c, err = Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.PrometheusListen != ":9101" {
		t.Errorf("PrometheusListen = %q, want :9101 from env", c.PrometheusListen)
	}
}

func TestQuietOutput(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"one-shot default prints", Config{}, false},
		{"explicit quiet", Config{Quiet: true}, true},
		{"daemon is quiet", Config{Daemon: true}, true},
		{"daemon json prints", Config{Daemon: true, JSON: true}, false},
		{"daemon json but quiet", Config{Daemon: true, JSON: true, Quiet: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.QuietOutput(); got != tt.want {
				t.Errorf("QuietOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}
