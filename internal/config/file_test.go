package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultConfigPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	got, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	want := filepath.Join("/tmp/xdg-test", "ey", "config.yaml")
	if got != want {
		t.Errorf("DefaultConfigPath = %q, want %q", got, want)
	}
}

func TestLoadFileMissingIsOK(t *testing.T) {
	f, err := LoadFile("")
	if err != nil || f != nil {
		t.Errorf("LoadFile(\"\") = %+v, %v; want nil, nil", f, err)
	}
	f, err = LoadFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil || f != nil {
		t.Errorf("LoadFile(absent) = %+v, %v; want nil, nil", f, err)
	}
}

func TestLoadFileMalformedErrors(t *testing.T) {
	path := writeConfigFile(t, "disk_threshold: [not, a, number\n")
	if _, err := LoadFile(path); err == nil {
		t.Fatal("LoadFile(malformed) = nil error, want error")
	}
}

func TestLoadFileIgnoresUnknownKeys(t *testing.T) {
	path := writeConfigFile(t, "disk_threshold: 42\nultimate_answer: 42\n")
	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if f.DiskThreshold == nil || *f.DiskThreshold != 42 {
		t.Errorf("DiskThreshold = %v, want 42", f.DiskThreshold)
	}
}

func TestFileApplyOverlay(t *testing.T) {
	path := writeConfigFile(t, `
disk_threshold: 42
interval: 30s
alert_mode: cooldown
color: always
prometheus_listen: ":9101"
`)
	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	c := builtinDefaults()
	f.applyTo(c)
	if c.DiskThreshold != 42 {
		t.Errorf("DiskThreshold = %g, want 42", c.DiskThreshold)
	}
	if c.MemThreshold != 90 {
		t.Errorf("MemThreshold = %g, want built-in 90", c.MemThreshold)
	}
	if c.Interval != 30*time.Second {
		t.Errorf("Interval = %s, want 30s", c.Interval)
	}
	if c.AlertMode != AlertCooldown {
		t.Errorf("AlertMode = %q, want cooldown", c.AlertMode)
	}
	if c.Color != ColorAlways {
		t.Errorf("Color = %q, want always", c.Color)
	}
	if c.PrometheusListen != ":9101" {
		t.Errorf("PrometheusListen = %q, want :9101", c.PrometheusListen)
	}
}

func TestFileMetricsScalarAndList(t *testing.T) {
	for _, tt := range []struct {
		yaml string
		want string
	}{
		{"metrics: cpu,disk\n", "cpu,disk"},
		{"metrics:\n  - memory\n", "memory"},
	} {
		f, err := LoadFile(writeConfigFile(t, tt.yaml))
		if err != nil {
			t.Fatalf("LoadFile(%q): %v", tt.yaml, err)
		}
		c := builtinDefaults()
		f.applyTo(c)
		if c.Metrics != tt.want {
			t.Errorf("Metrics = %q, want %q", c.Metrics, tt.want)
		}
	}
}

func TestFileInvalidDurationErrors(t *testing.T) {
	path := writeConfigFile(t, "interval: thirty\n")
	if _, err := LoadFile(path); err == nil {
		t.Fatal("LoadFile(bad duration) = nil error, want error")
	}
}

func TestRoundTripWriteAndLoad(t *testing.T) {
	c := &Config{
		DiskThreshold: 55.5,
		MemThreshold:  70,
		CPUThreshold:  60,
		Path:          "/var",
		Webhook:       "https://hooks.slack.com/services/x",
		Format:        FormatDiscord,
		Daemon:        true,
		Interval:      30 * time.Second,
		AlertMode:     AlertCooldown,
		Cooldown:      5 * time.Minute,
		Timeout:       2 * time.Second,
		JSON:          true,
		ExitCode:      true,
		LogFormat:     LogFormatJSON,
		LogLevel:      LogLevelDebug,
		Quiet:         true,
		Retries:       1,
		RetryBackoff:  2 * time.Second,
		StateFile:     "/tmp/state.json",
		NoState:       true,
		Metrics:       "cpu,memory",
		Color:         ColorNever,

		ProcessCount:          7,
		ProcessSort:           ProcessSortMem,
		ProcessThreshold:      65,
		ProcessCountThreshold: 300,

		PrometheusListen: ":9101",
	}

	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := WriteFile(path, FromConfig(c)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perm = %o, want 600", perm)
	}

	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	got := builtinDefaults()
	f.applyTo(got)
	if !reflect.DeepEqual(got, c) {
		t.Errorf("round-tripped config differs:\n got: %+v\nwant: %+v", got, c)
	}
}

func TestPrecedenceFileEnvFlag(t *testing.T) {
	clearEnv(t)
	path := writeConfigFile(t, "disk_threshold: 42\n")
	t.Setenv("EY_CONFIG", path)

	c, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DiskThreshold != 42 {
		t.Errorf("file-only DiskThreshold = %g, want 42", c.DiskThreshold)
	}
	if c.ConfigPath != path {
		t.Errorf("ConfigPath = %q, want %q", c.ConfigPath, path)
	}

	t.Setenv("EY_DISK_THRESHOLD", "55")
	c, err = Parse(nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DiskThreshold != 55 {
		t.Errorf("env-over-file DiskThreshold = %g, want 55", c.DiskThreshold)
	}

	c, err = Parse([]string{"-disk-threshold", "70"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.DiskThreshold != 70 {
		t.Errorf("flag-over-env DiskThreshold = %g, want 70", c.DiskThreshold)
	}
}

func TestProcessMetricAutoEnableFromFile(t *testing.T) {
	hasProcess := func(metrics string) bool {
		for _, m := range strings.Split(metrics, ",") {
			if m == "process" {
				return true
			}
		}
		return false
	}

	tests := []struct {
		name string
		yaml string
		args []string
		want bool
	}{
		{"file process option, no metrics", "process_count: 3\n", nil, true},
		{"file has both metrics and process", "metrics: cpu,memory\nprocess_count: 3\n", nil, false},
		{"file metrics includes process", "metrics: process\n", nil, true},
		{"file process option, metrics flag excludes", "process_count: 3\n", []string{"-metrics", "cpu"}, false},
		{"file metrics, process flag includes", "metrics: cpu,memory\n", []string{"-process-count", "3"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("EY_CONFIG", writeConfigFile(t, tt.yaml))
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

func TestMarshalIncludesAllKeys(t *testing.T) {
	data, err := Marshal(builtinDefaults())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{
		"disk_threshold", "mem_threshold", "cpu_threshold", "path", "metrics",
		"interval", "alert_mode", "cooldown", "timeout", "retries",
		"retry_backoff", "color",
		"process_count", "process_sort", "process_threshold", "process_count_threshold",
		"prometheus_listen",
	} {
		if !strings.Contains(string(data), key) {
			t.Errorf("marshalled config missing key %q", key)
		}
	}
}
