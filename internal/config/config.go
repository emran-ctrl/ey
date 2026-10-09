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

// BuildVersion is injected at build time via -ldflags "-X ey/internal/config.BuildVersion=...".
var BuildVersion = "dev"

type Config struct {
	DiskThreshold float64
	MemThreshold  float64
	CPUThreshold  float64

	Path string

	Webhook string
	Format  Format

	Daemon   bool
	Interval time.Duration

	AlertMode AlertMode
	Cooldown  time.Duration

	Timeout time.Duration

	JSON        bool
	ExitCode    bool
	ShowVersion bool
}

// envErr records an environment value that could not be parsed. The error is
// only reported if the corresponding flag was not set explicitly, since an
// explicit flag takes precedence over the environment.
type envErr struct {
	flag string
	err  error
}

// register binds the Config fields to fs. Defaults come from the environment
// (EY_*) so that flags override environment values, which in turn override
// built-in defaults. The returned closure applies the string-backed enum
// values after parsing.
func (c *Config) register(fs *flag.FlagSet) (applyEnums func(), envErrs []envErr) {
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

	fs.Float64Var(&c.DiskThreshold, "disk-threshold", floatDefault("disk-threshold", "EY_DISK_THRESHOLD", 80), "Disk usage percentage threshold to trigger alert")
	fs.Float64Var(&c.MemThreshold, "mem-threshold", floatDefault("mem-threshold", "EY_MEM_THRESHOLD", 90), "Memory usage percentage threshold to trigger alert")
	fs.Float64Var(&c.CPUThreshold, "cpu-threshold", floatDefault("cpu-threshold", "EY_CPU_THRESHOLD", 90), "CPU usage percentage threshold to trigger alert")

	fs.StringVar(&c.Path, "path", stringDefault("EY_PATH", "/"), "Filesystem path to check disk usage for")

	fs.StringVar(&c.Webhook, "webhook", stringDefault("EY_WEBHOOK", ""), "Slack, Discord, or generic webhook URL")
	format := stringDefault("EY_FORMAT", string(FormatAuto))
	fs.StringVar(&format, "format", format, "Webhook payload format: auto, slack, discord, or generic")

	fs.BoolVar(&c.Daemon, "daemon", boolDefault("daemon", "EY_DAEMON", false), "Run continuously instead of a single check")
	fs.DurationVar(&c.Interval, "interval", durationDefault("interval", "EY_INTERVAL", 60*time.Second), "Interval between checks in daemon mode")

	mode := stringDefault("EY_ALERT_MODE", string(AlertAlways))
	fs.StringVar(&mode, "alert-mode", mode, "Alert policy: always, once, or cooldown")
	fs.DurationVar(&c.Cooldown, "cooldown", durationDefault("cooldown", "EY_COOLDOWN", 10*time.Minute), "Minimum time between alerts in cooldown mode")

	fs.DurationVar(&c.Timeout, "timeout", durationDefault("timeout", "EY_TIMEOUT", 5*time.Second), "HTTP client timeout for webhook delivery")

	fs.BoolVar(&c.JSON, "json", boolDefault("json", "EY_JSON", false), "Emit metrics as JSON to stdout")
	fs.BoolVar(&c.ExitCode, "exit-code", boolDefault("exit-code", "EY_EXIT_CODE", false), "Exit with code 1 when any threshold is breached")
	fs.BoolVar(&c.ShowVersion, "version", false, "Print version and exit")

	return func() {
		c.Format = Format(format)
		c.AlertMode = AlertMode(mode)
	}, envErrs
}

// Parse parses args (without the program name) into a validated Config.
func Parse(args []string) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("ey", flag.ContinueOnError)
	applyEnums, envErrs := c.register(fs)
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
