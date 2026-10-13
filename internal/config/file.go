package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"

	"ey/internal/fsutil"
)

// File is the on-disk YAML representation of a configuration. Every field is a
// pointer so that an absent key is distinguishable from an explicit zero value,
// which lets the file sit between built-in defaults and the environment in the
// precedence chain.
type File struct {
	DiskThreshold *float64 `yaml:"disk_threshold,omitempty"`
	MemThreshold  *float64 `yaml:"mem_threshold,omitempty"`
	CPUThreshold  *float64 `yaml:"cpu_threshold,omitempty"`

	Path *string `yaml:"path,omitempty"`

	Webhook *string `yaml:"webhook,omitempty"`
	Format  *string `yaml:"format,omitempty"`

	Daemon   *bool     `yaml:"daemon,omitempty"`
	Interval *Duration `yaml:"interval,omitempty"`

	AlertMode *string   `yaml:"alert_mode,omitempty"`
	Cooldown  *Duration `yaml:"cooldown,omitempty"`

	Timeout *Duration `yaml:"timeout,omitempty"`

	JSON     *bool `yaml:"json,omitempty"`
	ExitCode *bool `yaml:"exit_code,omitempty"`

	LogFormat *string `yaml:"log_format,omitempty"`
	LogLevel  *string `yaml:"log_level,omitempty"`
	Quiet     *bool   `yaml:"quiet,omitempty"`

	Retries      *int      `yaml:"retries,omitempty"`
	RetryBackoff *Duration `yaml:"retry_backoff,omitempty"`

	StateFile *string `yaml:"state_file,omitempty"`
	NoState   *bool   `yaml:"no_state,omitempty"`

	Metrics *metricList `yaml:"metrics,omitempty"`
	Color   *string     `yaml:"color,omitempty"`

	PrometheusListen *string `yaml:"prometheus_listen,omitempty"`
}

// Duration marshals to and from a Go duration string such as "30s" or "10m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(dur)
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// metricList accepts either a YAML list (metrics: [disk, cpu]) or a
// comma-separated scalar (metrics: disk,cpu).
type metricList []string

func (m *metricList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*m = splitList(s)
		return nil
	}
	var list []string
	if err := node.Decode(&list); err != nil {
		return err
	}
	*m = list
	return nil
}

// DefaultConfigPath returns the per-user configuration file location,
// $XDG_CONFIG_HOME/ey/config.yaml on Linux. Callers may override it with the
// -config flag or the EY_CONFIG environment variable.
func DefaultConfigPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "ey", "config.yaml"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(dir, "ey", "config.yaml"), nil
}

// LoadFile reads and parses the config file at path. A missing file yields
// (nil, nil) so callers can treat it as "no file configured".
func LoadFile(path string) (*File, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &f, nil
}

// builtinDefaults returns the configuration used when nothing else is set.
func builtinDefaults() *Config {
	return &Config{
		DiskThreshold: 80,
		MemThreshold:  90,
		CPUThreshold:  90,
		Path:          "/",
		Format:        FormatAuto,
		Interval:      60 * time.Second,
		AlertMode:     AlertAlways,
		Cooldown:      10 * time.Minute,
		Timeout:       5 * time.Second,
		LogFormat:     LogFormatText,
		LogLevel:      LogLevelInfo,
		Retries:       3,
		RetryBackoff:  500 * time.Millisecond,
		Metrics:       DefaultMetrics,
		Color:         ColorAuto,
	}
}

// applyTo overlays the file's explicitly set fields onto c.
func (f *File) applyTo(c *Config) {
	if f == nil {
		return
	}
	setFloat(&c.DiskThreshold, f.DiskThreshold)
	setFloat(&c.MemThreshold, f.MemThreshold)
	setFloat(&c.CPUThreshold, f.CPUThreshold)
	setString(&c.Path, f.Path)
	setString(&c.Webhook, f.Webhook)
	setEnum(&c.Format, f.Format)
	setBool(&c.Daemon, f.Daemon)
	setDuration(&c.Interval, f.Interval)
	setEnum(&c.AlertMode, f.AlertMode)
	setDuration(&c.Cooldown, f.Cooldown)
	setDuration(&c.Timeout, f.Timeout)
	setBool(&c.JSON, f.JSON)
	setBool(&c.ExitCode, f.ExitCode)
	setEnum(&c.LogFormat, f.LogFormat)
	setEnum(&c.LogLevel, f.LogLevel)
	setBool(&c.Quiet, f.Quiet)
	setInt(&c.Retries, f.Retries)
	setDuration(&c.RetryBackoff, f.RetryBackoff)
	setString(&c.StateFile, f.StateFile)
	setBool(&c.NoState, f.NoState)
	if f.Metrics != nil {
		c.Metrics = joinList(*f.Metrics)
	}
	setEnum(&c.Color, f.Color)
	setString(&c.PrometheusListen, f.PrometheusListen)
}

// Load returns the effective configuration from built-in defaults merged with
// the file at path, ignoring flags and the environment. The interactive wizard
// uses it to seed prompts.
func Load(path string) (*Config, error) {
	file, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	c := builtinDefaults()
	file.applyTo(c)
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// FromConfig builds a File capturing every field of c, so a written file is a
// complete, hand-editable snapshot.
func FromConfig(c *Config) *File {
	return &File{
		DiskThreshold:    ptr(c.DiskThreshold),
		MemThreshold:     ptr(c.MemThreshold),
		CPUThreshold:     ptr(c.CPUThreshold),
		Path:             ptr(c.Path),
		Webhook:          ptr(c.Webhook),
		Format:           ptr(string(c.Format)),
		Daemon:           ptr(c.Daemon),
		Interval:         durPtr(c.Interval),
		AlertMode:        ptr(string(c.AlertMode)),
		Cooldown:         durPtr(c.Cooldown),
		Timeout:          durPtr(c.Timeout),
		JSON:             ptr(c.JSON),
		ExitCode:         ptr(c.ExitCode),
		LogFormat:        ptr(string(c.LogFormat)),
		LogLevel:         ptr(string(c.LogLevel)),
		Quiet:            ptr(c.Quiet),
		Retries:          ptr(c.Retries),
		RetryBackoff:     durPtr(c.RetryBackoff),
		StateFile:        ptr(c.StateFile),
		NoState:          ptr(c.NoState),
		Metrics:          metricsPtr(c.Metrics),
		Color:            ptr(string(c.Color)),
		PrometheusListen: ptr(c.PrometheusListen),
	}
}

// Marshal serializes c as YAML, for previews such as "ey config -print".
func Marshal(c *Config) ([]byte, error) {
	data, err := yaml.Marshal(FromConfig(c))
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	return data, nil
}

// WriteFile marshals f to YAML and writes it atomically with mode 0600.
func WriteFile(path string, f *File) error {
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func durPtr(d time.Duration) *Duration {
	v := Duration(d)
	return &v
}

func metricsPtr(s string) *metricList {
	list := metricList(splitList(s))
	return &list
}

func setFloat(dst *float64, src *float64) {
	if src != nil {
		*dst = *src
	}
}

func setString(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

func setBool(dst *bool, src *bool) {
	if src != nil {
		*dst = *src
	}
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func setDuration(dst *time.Duration, src *Duration) {
	if src != nil {
		*dst = time.Duration(*src)
	}
}

func setEnum[T ~string](dst *T, src *string) {
	if src != nil {
		*dst = T(*src)
	}
}
