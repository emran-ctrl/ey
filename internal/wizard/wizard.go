// Package wizard provides an interactive, line-based editor for ey's
// configuration file. It reads prompts from a reader and writes them to a
// writer, so it is fully scriptable and testable. Every value is validated and
// re-prompted on error; pressing Enter keeps the current value.
package wizard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ey/internal/config"
)

// ErrAborted is returned when the user declines to save the new configuration.
var ErrAborted = errors.New("configuration change aborted")

// DefaultMetrics is the metric set used to prompt when no selection is current.
const DefaultMetrics = "disk,memory,cpu"

type wizard struct {
	in  *bufio.Scanner
	out io.Writer
}

// Run walks through every setting, seeded with base (the current effective
// configuration), and returns the updated configuration. The caller writes it
// to disk. It returns ErrAborted if the user declines to save.
func Run(in io.Reader, out io.Writer, base *config.Config) (*config.Config, error) {
	w := &wizard{in: bufio.NewScanner(in), out: out}
	cfg := *base

	fmt.Fprintln(out, "Configuring ey. Enter a value at each prompt, or press Enter to keep the current value in [brackets]. Ctrl-C cancels.")
	fmt.Fprintln(out)

	var err error

	// Thresholds.
	if cfg.DiskThreshold, err = w.askFloat("Disk usage threshold (%)", cfg.DiskThreshold, 0, 100); err != nil {
		return nil, err
	}
	if cfg.MemThreshold, err = w.askFloat("Memory usage threshold (%)", cfg.MemThreshold, 0, 100); err != nil {
		return nil, err
	}
	if cfg.CPUThreshold, err = w.askFloat("CPU usage threshold (%)", cfg.CPUThreshold, 0, 100); err != nil {
		return nil, err
	}

	// Monitoring.
	if cfg.Path, err = w.ask("Filesystem path to check disk usage", cfg.Path, func(s string) error {
		if s == "" {
			return errors.New("path must not be empty")
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if cfg.Metrics, err = w.askMetrics(cfg.Metrics); err != nil {
		return nil, err
	}

	var v string
	if cfg.ProcessCount, err = w.askInt("Top processes to list (process metric)", cfg.ProcessCount, 1); err != nil {
		return nil, err
	}
	if v, err = w.askEnum("Rank top processes by", string(cfg.ProcessSort), "cpu", "memory"); err != nil {
		return nil, err
	}
	cfg.ProcessSort = config.ProcessSort(v)
	if cfg.ProcessThreshold, err = w.askFloat("Top process usage threshold (%)", cfg.ProcessThreshold, 0, 100); err != nil {
		return nil, err
	}
	if cfg.ProcessCountThreshold, err = w.askInt("Process count threshold (0 to disable)", cfg.ProcessCountThreshold, 0); err != nil {
		return nil, err
	}

	if v, err = w.askEnum("Webhook payload format", string(cfg.Format), "auto", "slack", "discord", "generic"); err != nil {
		return nil, err
	}
	cfg.Format = config.Format(v)

	// Alerting.
	if cfg.Webhook, err = w.ask("Webhook URL (Slack/Discord/generic; empty to disable)", cfg.Webhook, func(s string) error {
		if s == "" {
			return nil
		}
		u, uerr := url.Parse(s)
		if uerr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("must be an absolute http(s) URL")
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if v, err = w.askEnum("Alert policy", string(cfg.AlertMode), "always", "once", "cooldown"); err != nil {
		return nil, err
	}
	cfg.AlertMode = config.AlertMode(v)
	if cfg.Cooldown, err = w.askDuration("Cooldown between alerts", cfg.Cooldown); err != nil {
		return nil, err
	}
	if cfg.Retries, err = w.askInt("Webhook retries after the first attempt", cfg.Retries, 0); err != nil {
		return nil, err
	}
	if cfg.RetryBackoff, err = w.askDuration("Retry backoff base delay", cfg.RetryBackoff); err != nil {
		return nil, err
	}
	if cfg.Timeout, err = w.askDuration("Webhook delivery timeout", cfg.Timeout); err != nil {
		return nil, err
	}

	// Output.
	if cfg.JSON, err = w.askBool("Emit metrics as JSON", cfg.JSON); err != nil {
		return nil, err
	}
	if v, err = w.askEnum("Colorize text output", string(cfg.Color), "auto", "always", "never"); err != nil {
		return nil, err
	}
	cfg.Color = config.ColorMode(v)

	// Logging.
	if v, err = w.askEnum("Log output format", string(cfg.LogFormat), "text", "json"); err != nil {
		return nil, err
	}
	cfg.LogFormat = config.LogFormat(v)
	if v, err = w.askEnum("Log level", string(cfg.LogLevel), "debug", "info", "warn", "error"); err != nil {
		return nil, err
	}
	cfg.LogLevel = config.LogLevel(v)
	if cfg.Quiet, err = w.askBool("Suppress metrics output", cfg.Quiet); err != nil {
		return nil, err
	}

	// Lifecycle.
	if cfg.Daemon, err = w.askBool("Run continuously (daemon mode)", cfg.Daemon); err != nil {
		return nil, err
	}
	if cfg.Interval, err = w.askDuration("Interval between checks in daemon mode", cfg.Interval); err != nil {
		return nil, err
	}
	if cfg.PrometheusListen, err = w.ask("Prometheus metrics listen address (host:port; empty to disable, daemon only)", cfg.PrometheusListen, func(s string) error {
		if s == "" {
			return nil
		}
		if verr := config.ValidateListenAddr(s); verr != nil {
			return verr
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if cfg.ExitCode, err = w.askBool("Exit with code 1 when any threshold is breached", cfg.ExitCode); err != nil {
		return nil, err
	}
	if cfg.StateFile, err = w.ask("State file path (empty = default location)", cfg.StateFile, nil); err != nil {
		return nil, err
	}
	if cfg.NoState, err = w.askBool("Disable persisting alert state to disk", cfg.NoState); err != nil {
		return nil, err
	}

	w.summarize(&cfg)

	ok, err := w.askBool("Write this configuration", true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrAborted
	}
	return &cfg, nil
}

func (w *wizard) askMetrics(current string) (string, error) {
	for {
		if current == "" {
			current = DefaultMetrics
		}
		fmt.Fprintf(w.out, "Metrics to monitor (comma-separated: disk, memory, cpu, process) [%s]: ", current)
		line, err := w.read()
		if err != nil {
			return "", err
		}
		if line == "" {
			return current, nil
		}
		norm, merr := config.NormalizeMetrics(line)
		if merr != nil {
			fmt.Fprintf(w.out, "  invalid: %v\n", merr)
			continue
		}
		return norm, nil
	}
}

func (w *wizard) ask(prompt, def string, validate func(string) error) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(w.out, "%s (default: %s): ", prompt, def)
		} else {
			fmt.Fprintf(w.out, "%s: ", prompt)
		}
		line, err := w.read()
		if err != nil {
			return "", err
		}
		if line == "" {
			line = def
		}
		if validate != nil {
			if verr := validate(line); verr != nil {
				fmt.Fprintf(w.out, "  invalid: %v\n", verr)
				continue
			}
		}
		return line, nil
	}
}

func (w *wizard) askEnum(prompt, cur string, choices ...string) (string, error) {
	for {
		fmt.Fprintf(w.out, "%s [%s] (%s): ", prompt, cur, strings.Join(choices, "/"))
		line, err := w.read()
		if err != nil {
			return "", err
		}
		if line == "" {
			return cur, nil
		}
		norm := strings.ToLower(strings.TrimSpace(line))
		for _, c := range choices {
			if norm == c {
				return norm, nil
			}
		}
		fmt.Fprintf(w.out, "  invalid: choose one of %s\n", strings.Join(choices, "/"))
	}
}

func (w *wizard) askFloat(prompt string, def, min, max float64) (float64, error) {
	for {
		fmt.Fprintf(w.out, "%s [%s]: ", prompt, strconv.FormatFloat(def, 'f', -1, 64))
		line, err := w.read()
		if err != nil {
			return 0, err
		}
		if line == "" {
			return def, nil
		}
		v, perr := strconv.ParseFloat(line, 64)
		if perr != nil {
			fmt.Fprintf(w.out, "  invalid: not a number\n")
			continue
		}
		if v < min || v > max {
			fmt.Fprintf(w.out, "  invalid: must be between %s and %s\n",
				strconv.FormatFloat(min, 'f', -1, 64), strconv.FormatFloat(max, 'f', -1, 64))
			continue
		}
		return v, nil
	}
}

func (w *wizard) askInt(prompt string, def, min int) (int, error) {
	for {
		fmt.Fprintf(w.out, "%s [%d]: ", prompt, def)
		line, err := w.read()
		if err != nil {
			return 0, err
		}
		if line == "" {
			return def, nil
		}
		v, perr := strconv.Atoi(line)
		if perr != nil {
			fmt.Fprintf(w.out, "  invalid: not an integer\n")
			continue
		}
		if v < min {
			fmt.Fprintf(w.out, "  invalid: must be at least %d\n", min)
			continue
		}
		return v, nil
	}
}

func (w *wizard) askDuration(prompt string, def time.Duration) (time.Duration, error) {
	for {
		fmt.Fprintf(w.out, "%s [%s]: ", prompt, def)
		line, err := w.read()
		if err != nil {
			return 0, err
		}
		if line == "" {
			return def, nil
		}
		v, perr := time.ParseDuration(line)
		if perr != nil || v <= 0 {
			fmt.Fprintf(w.out, "  invalid: must be a positive duration like 5s or 10m\n")
			continue
		}
		return v, nil
	}
}

func (w *wizard) askBool(prompt string, def bool) (bool, error) {
	for {
		fmt.Fprintf(w.out, "%s [%s]: ", prompt, boolLabel(def))
		line, err := w.read()
		if err != nil {
			return false, err
		}
		if line == "" {
			return def, nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes", "true", "1":
			return true, nil
		case "n", "no", "false", "0":
			return false, nil
		default:
			fmt.Fprintf(w.out, "  invalid: answer yes or no\n")
		}
	}
}

func (w *wizard) read() (string, error) {
	if !w.in.Scan() {
		if err := w.in.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return strings.TrimSpace(w.in.Text()), nil
}

func boolLabel(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (w *wizard) summarize(cfg *config.Config) {
	fmt.Fprintln(w.out)
	fmt.Fprintln(w.out, "Summary")
	fmt.Fprintln(w.out, "-------")
	lines := []string{
		fmt.Sprintf("Disk threshold:      %.2f%%", cfg.DiskThreshold),
		fmt.Sprintf("Memory threshold:    %.2f%%", cfg.MemThreshold),
		fmt.Sprintf("CPU threshold:       %.2f%%", cfg.CPUThreshold),
		fmt.Sprintf("Path:                %s", cfg.Path),
		fmt.Sprintf("Metrics:             %s", cfg.Metrics),
		fmt.Sprintf("Top processes:       %d", cfg.ProcessCount),
		fmt.Sprintf("Process sort:        %s", cfg.ProcessSort),
		fmt.Sprintf("Process threshold:   %.2f%%", cfg.ProcessThreshold),
		fmt.Sprintf("Process count max:   %d", cfg.ProcessCountThreshold),
		fmt.Sprintf("Webhook:             %s", orDefault(cfg.Webhook)),
		fmt.Sprintf("Format:              %s", cfg.Format),
		fmt.Sprintf("Alert policy:        %s", cfg.AlertMode),
		fmt.Sprintf("Retries:             %d", cfg.Retries),
		fmt.Sprintf("Retry backoff:       %s", cfg.RetryBackoff),
		fmt.Sprintf("Timeout:             %s", cfg.Timeout),
		fmt.Sprintf("Daemon:              %t", cfg.Daemon),
		fmt.Sprintf("Interval:            %s", cfg.Interval),
		fmt.Sprintf("Prometheus listen:   %s", orDefault(cfg.PrometheusListen)),
		fmt.Sprintf("JSON output:         %t", cfg.JSON),
		fmt.Sprintf("Color:               %s", cfg.Color),
		fmt.Sprintf("Log format:          %s", cfg.LogFormat),
		fmt.Sprintf("Log level:           %s", cfg.LogLevel),
		fmt.Sprintf("Quiet:               %t", cfg.Quiet),
		fmt.Sprintf("Exit code on breach: %t", cfg.ExitCode),
		fmt.Sprintf("No state:            %t", cfg.NoState),
	}
	for _, l := range lines {
		fmt.Fprintln(w.out, l)
	}
}

func orDefault(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
