package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type AlertMode string

const (
	AlertAlways   AlertMode = "always"
	AlertOnce     AlertMode = "once"
	AlertCooldown AlertMode = "cooldown"
)

type Format string

const (
	FormatAuto    Format = "auto"
	FormatSlack   Format = "slack"
	FormatDiscord Format = "discord"
	FormatGeneric Format = "generic"
)

type LogFormat string

const (
	LogFormatText LogFormat = "text"
	LogFormatJSON LogFormat = "json"
)

type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

type ColorMode string

const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

// DefaultMetrics is the set of metrics collected when none is configured.
const DefaultMetrics = "disk,memory,cpu"

// metricOrder is the canonical ordering used to normalize metric selections.
var metricOrder = []string{"disk", "memory", "cpu"}

// BuildVersion is injected at build time via -ldflags "-X ey/internal/config.BuildVersion=...".
var BuildVersion = "dev"

type Config struct {
	DiskThreshold float64
	MemThreshold  float64
	CPUThreshold  float64

	Path       string
	ConfigPath string

	Webhook string
	Format  Format

	Metrics string
	Color   ColorMode

	Daemon   bool
	Interval time.Duration

	AlertMode AlertMode
	Cooldown  time.Duration

	Timeout time.Duration

	JSON        bool
	ExitCode    bool
	ShowVersion bool

	LogFormat LogFormat
	LogLevel  LogLevel
	Quiet     bool

	Retries      int
	RetryBackoff time.Duration

	StateFile string
	NoState   bool
}

// envErr records an environment value that could not be parsed. The error is
// only reported if the corresponding flag was not set explicitly, since an
// explicit flag takes precedence over the environment.
type envErr struct {
	flag string
	err  error
}

// QuietOutput reports whether the metrics block should be suppressed on stdout.
// Daemon mode is quiet unless JSON output is explicitly requested, so normal
// daemon operation relies on structured logs instead of periodic stdout spam.
func (c *Config) QuietOutput() bool {
	return c.Quiet || (c.Daemon && !c.JSON)
}

// register binds the Config fields to fs. Defaults come from d, which already
// holds built-in defaults overlaid with the config file; the environment
// (EY_*) is applied on top, and flags on top of that. The returned closure
// applies the string-backed enum values after parsing.
func (c *Config) register(fs *flag.FlagSet, d *Config, configPath string) (applyEnums func(), envErrs []envErr) {
	floatDefault := func(flagName, key string, def float64) float64 {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return def
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			envErrs = append(envErrs, envErr{flagName, fmt.Errorf("invalid %s=%q: not a number", key, v)})
			return def
		}
		return f
	}
	durationDefault := func(flagName, key string, def time.Duration) time.Duration {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			envErrs = append(envErrs, envErr{flagName, fmt.Errorf("invalid %s=%q: not a duration", key, v)})
			return def
		}
		return d
	}
	boolDefault := func(flagName, key string, def bool) bool {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			envErrs = append(envErrs, envErr{flagName, fmt.Errorf("invalid %s=%q: not a boolean", key, v)})
			return def
		}
		return b
	}
	stringDefault := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return def
	}
	intDefault := func(flagName, key string, def int) int {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			envErrs = append(envErrs, envErr{flagName, fmt.Errorf("invalid %s=%q: not an integer", key, v)})
			return def
		}
		return n
	}

	fs.Float64Var(&c.DiskThreshold, "disk-threshold", floatDefault("disk-threshold", "EY_DISK_THRESHOLD", d.DiskThreshold), "Disk usage percentage threshold to trigger alert")
	fs.Float64Var(&c.MemThreshold, "mem-threshold", floatDefault("mem-threshold", "EY_MEM_THRESHOLD", d.MemThreshold), "Memory usage percentage threshold to trigger alert")
	fs.Float64Var(&c.CPUThreshold, "cpu-threshold", floatDefault("cpu-threshold", "EY_CPU_THRESHOLD", d.CPUThreshold), "CPU usage percentage threshold to trigger alert")

	fs.StringVar(&c.Path, "path", stringDefault("EY_PATH", d.Path), "Filesystem path to check disk usage for")

	fs.StringVar(&c.Webhook, "webhook", stringDefault("EY_WEBHOOK", d.Webhook), "Slack, Discord, or generic webhook URL")
	format := stringDefault("EY_FORMAT", string(d.Format))
	fs.StringVar(&format, "format", format, "Webhook payload format: auto, slack, discord, or generic")

	fs.BoolVar(&c.Daemon, "daemon", boolDefault("daemon", "EY_DAEMON", d.Daemon), "Run continuously instead of a single check")
	fs.DurationVar(&c.Interval, "interval", durationDefault("interval", "EY_INTERVAL", d.Interval), "Interval between checks in daemon mode")

	mode := stringDefault("EY_ALERT_MODE", string(d.AlertMode))
	fs.StringVar(&mode, "alert-mode", mode, "Alert policy: always, once, or cooldown")
	fs.DurationVar(&c.Cooldown, "cooldown", durationDefault("cooldown", "EY_COOLDOWN", d.Cooldown), "Minimum time between alerts in cooldown mode")

	fs.DurationVar(&c.Timeout, "timeout", durationDefault("timeout", "EY_TIMEOUT", d.Timeout), "HTTP client timeout for webhook delivery")

	fs.BoolVar(&c.JSON, "json", boolDefault("json", "EY_JSON", d.JSON), "Emit metrics as JSON to stdout")
	fs.BoolVar(&c.ExitCode, "exit-code", boolDefault("exit-code", "EY_EXIT_CODE", d.ExitCode), "Exit with code 1 when any threshold is breached")
	fs.BoolVar(&c.ShowVersion, "version", false, "Print version and exit")

	logFormat := stringDefault("EY_LOG_FORMAT", string(d.LogFormat))
	fs.StringVar(&logFormat, "log-format", logFormat, "Log output format: text or json")
	logLevel := stringDefault("EY_LOG_LEVEL", string(d.LogLevel))
	fs.StringVar(&logLevel, "log-level", logLevel, "Log level: debug, info, warn, or error")
	fs.BoolVar(&c.Quiet, "quiet", boolDefault("quiet", "EY_QUIET", d.Quiet), "Suppress metrics output (logs still go to stderr)")

	fs.IntVar(&c.Retries, "retries", intDefault("retries", "EY_RETRIES", d.Retries), "Webhook delivery retries after the first attempt (0 disables)")
	fs.DurationVar(&c.RetryBackoff, "retry-backoff", durationDefault("retry-backoff", "EY_RETRY_BACKOFF", d.RetryBackoff), "Base delay for webhook retry backoff")

	fs.StringVar(&c.StateFile, "state-file", stringDefault("EY_STATE_FILE", d.StateFile), "Path to the alert state file (default: per-user state dir)")
	fs.BoolVar(&c.NoState, "no-state", boolDefault("no-state", "EY_NO_STATE", d.NoState), "Disable persisting alert state to disk")

	metrics := stringDefault("EY_METRICS", d.Metrics)
	fs.StringVar(&metrics, "metrics", metrics, "Comma-separated metrics to monitor: disk, memory, cpu")
	color := stringDefault("EY_COLOR", string(d.Color))
	fs.StringVar(&color, "color", color, "Colorize text output: auto, always, or never")

	fs.StringVar(&c.ConfigPath, "config", configPath, "Path to the YAML configuration file")

	return func() {
		c.Format = Format(format)
		c.AlertMode = AlertMode(mode)
		c.LogFormat = LogFormat(logFormat)
		c.LogLevel = LogLevel(logLevel)
		c.Metrics = metrics
		c.Color = ColorMode(color)
	}, envErrs
}

// resolveConfigPath determines which config file to read, honoring the -config
// flag first, then EY_CONFIG, then the per-user default location.
func resolveConfigPath(args []string) string {
	if v := scanFlagValue(args, "config"); v != "" {
		return v
	}
	if v := os.Getenv("EY_CONFIG"); v != "" {
		return v
	}
	if p, err := DefaultConfigPath(); err == nil {
		return p
	}
	return ""
}

// scanFlagValue returns the value of the named flag from args without parsing
// the whole set, supporting -name value, -name=value, and --name forms.
func scanFlagValue(args []string, name string) string {
	short, long := "-"+name, "--"+name
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == short || a == long {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if v, ok := strings.CutPrefix(a, short+"="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, long+"="); ok {
			return v
		}
	}
	return ""
}

// splitList splits a comma- or whitespace-separated list, dropping empties.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func joinList(list []string) string {
	return strings.Join(list, ",")
}

// NormalizeMetrics validates a metric selection and returns its canonical,
// deduplicated, comma-separated form.
func NormalizeMetrics(s string) (string, error) {
	seen := map[string]bool{}
	for _, raw := range splitList(s) {
		m := strings.ToLower(strings.TrimSpace(raw))
		switch m {
		case "disk", "memory", "cpu":
			seen[m] = true
		default:
			return "", fmt.Errorf("invalid -metrics entry %q: must be disk, memory, or cpu", raw)
		}
	}
	if len(seen) == 0 {
		return "", fmt.Errorf("-metrics must include at least one of disk, memory, or cpu")
	}
	out := make([]string, 0, len(seen))
	for _, m := range metricOrder {
		if seen[m] {
			out = append(out, m)
		}
	}
	return strings.Join(out, ","), nil
}

// Parse parses args (without the program name) into a validated Config.
func Parse(args []string) (*Config, error) {
	path := resolveConfigPath(args)
	file, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	defaults := builtinDefaults()
	file.applyTo(defaults)

	c := &Config{}
	fs := flag.NewFlagSet("ey", flag.ContinueOnError)
	applyEnums, envErrs := c.register(fs, defaults, path)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	applyEnums()

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var envMessages []string
	for _, e := range envErrs {
		if !set[e.flag] {
			envMessages = append(envMessages, e.err.Error())
		}
	}
	if len(envMessages) > 0 {
		fs.Usage()
		return nil, errors.New(strings.Join(envMessages, "; "))
	}

	if err := c.validate(); err != nil {
		fs.Usage()
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	for _, t := range []struct {
		name string
		val  float64
	}{
		{"disk-threshold", c.DiskThreshold},
		{"mem-threshold", c.MemThreshold},
		{"cpu-threshold", c.CPUThreshold},
	} {
		if t.val < 0 || t.val > 100 {
			return fmt.Errorf("%s must be between 0 and 100, got %g", t.name, t.val)
		}
	}

	switch Format(strings.ToLower(string(c.Format))) {
	case FormatAuto, FormatSlack, FormatDiscord, FormatGeneric:
		c.Format = Format(strings.ToLower(string(c.Format)))
	default:
		return fmt.Errorf("invalid -format %q: must be auto, slack, discord, or generic", c.Format)
	}

	switch AlertMode(strings.ToLower(string(c.AlertMode))) {
	case AlertAlways, AlertOnce, AlertCooldown:
		c.AlertMode = AlertMode(strings.ToLower(string(c.AlertMode)))
	default:
		return fmt.Errorf("invalid -alert-mode %q: must be always, once, or cooldown", c.AlertMode)
	}

	switch LogFormat(strings.ToLower(string(c.LogFormat))) {
	case LogFormatText, LogFormatJSON:
		c.LogFormat = LogFormat(strings.ToLower(string(c.LogFormat)))
	default:
		return fmt.Errorf("invalid -log-format %q: must be text or json", c.LogFormat)
	}

	switch LogLevel(strings.ToLower(string(c.LogLevel))) {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
		c.LogLevel = LogLevel(strings.ToLower(string(c.LogLevel)))
	default:
		return fmt.Errorf("invalid -log-level %q: must be debug, info, warn, or error", c.LogLevel)
	}

	metrics, err := NormalizeMetrics(c.Metrics)
	if err != nil {
		return err
	}
	c.Metrics = metrics

	switch ColorMode(strings.ToLower(string(c.Color))) {
	case ColorAuto, ColorAlways, ColorNever:
		c.Color = ColorMode(strings.ToLower(string(c.Color)))
	default:
		return fmt.Errorf("invalid -color %q: must be auto, always, or never", c.Color)
	}

	if c.Retries < 0 {
		return fmt.Errorf("-retries must not be negative, got %d", c.Retries)
	}
	if c.RetryBackoff <= 0 {
		return fmt.Errorf("-retry-backoff must be greater than 0, got %s", c.RetryBackoff)
	}

	if c.Interval <= 0 {
		return fmt.Errorf("-interval must be greater than 0, got %s", c.Interval)
	}
	if c.Cooldown <= 0 {
		return fmt.Errorf("-cooldown must be greater than 0, got %s", c.Cooldown)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("-timeout must be greater than 0, got %s", c.Timeout)
	}
	if c.Path == "" {
		return fmt.Errorf("-path must not be empty")
	}
	if c.Webhook != "" {
		u, err := url.Parse(c.Webhook)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("invalid -webhook URL %q: must be an absolute http(s) URL", c.Webhook)
		}
	}
	return nil
}
