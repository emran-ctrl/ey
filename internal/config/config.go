package config

import (
	"flag"
	"fmt"
	"net/url"
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

	JSON     bool
	ExitCode bool
}

// register binds the Config fields to fs. String-backed enum fields are
// collected into the returned closure's captured variables and applied by
// applyEnums after parsing.
func (c *Config) register(fs *flag.FlagSet) (applyEnums func()) {
	fs.Float64Var(&c.DiskThreshold, "disk-threshold", 80, "Disk usage percentage threshold to trigger alert")
	fs.Float64Var(&c.MemThreshold, "mem-threshold", 90, "Memory usage percentage threshold to trigger alert")
	fs.Float64Var(&c.CPUThreshold, "cpu-threshold", 90, "CPU usage percentage threshold to trigger alert")

	fs.StringVar(&c.Path, "path", "/", "Filesystem path to check disk usage for")

	fs.StringVar(&c.Webhook, "webhook", "", "Slack, Discord, or generic webhook URL")
	format := string(c.Format)
	fs.StringVar(&format, "format", string(FormatAuto), "Webhook payload format: auto, slack, discord, or generic")

	fs.BoolVar(&c.Daemon, "daemon", false, "Run continuously instead of a single check")
	fs.DurationVar(&c.Interval, "interval", 60*time.Second, "Interval between checks in daemon mode")

	mode := string(c.AlertMode)
	fs.StringVar(&mode, "alert-mode", string(AlertAlways), "Alert policy: always, once, or cooldown")
	fs.DurationVar(&c.Cooldown, "cooldown", 10*time.Minute, "Minimum time between alerts in cooldown mode")

	fs.DurationVar(&c.Timeout, "timeout", 5*time.Second, "HTTP client timeout for webhook delivery")

	fs.BoolVar(&c.JSON, "json", false, "Emit metrics as JSON to stdout")
	fs.BoolVar(&c.ExitCode, "exit-code", false, "Exit with code 1 when any threshold is breached")

	return func() {
		c.Format = Format(format)
		c.AlertMode = AlertMode(mode)
	}
}

// Parse parses args (without the program name) into a validated Config.
func Parse(args []string) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("ey", flag.ContinueOnError)
	applyEnums := c.register(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	applyEnums()
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
