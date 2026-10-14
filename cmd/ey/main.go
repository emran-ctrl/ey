package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ey/internal/alerter"
	"ey/internal/config"
	"ey/internal/monitor"
	"ey/internal/promexporter"
	"ey/internal/state"
	"ey/internal/wizard"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "config" {
		return runConfig(args[1:])
	}

	cfg, err := config.Parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "ey: %v\n", err)
		return 2
	}

	if cfg.ShowVersion {
		fmt.Printf("ey %s\n", config.BuildVersion)
		return 0
	}

	logger := newLogger(cfg)

	sel, err := monitor.ParseSelection(cfg.Metrics)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ey: %v\n", err)
		return 2
	}

	if cfg.PrometheusListen != "" && !cfg.Daemon {
		fmt.Fprintln(os.Stderr, "ey: -prometheus-listen requires -daemon")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openState(cfg, logger)
	if err != nil {
		logger.Error("state unavailable; continuing without persistence", "err", err)
	}

	client := alerter.New(cfg.Webhook, alerter.Options{
		Format:     cfg.Format,
		Timeout:    cfg.Timeout,
		Retries:    cfg.Retries,
		Backoff:    cfg.RetryBackoff,
		MaxBackoff: 8 * time.Second,
		Logger:     logger,
	})
	policy := newAlertPolicy(cfg, store, logger)

	thresholds := monitor.Thresholds{
		Disk:         cfg.DiskThreshold,
		Mem:          cfg.MemThreshold,
		CPU:          cfg.CPUThreshold,
		Process:      cfg.ProcessThreshold,
		ProcessCount: cfg.ProcessCountThreshold,
	}

	if cfg.PrometheusListen != "" {
		exp := promexporter.New(promexporter.Options{
			Selection:  sel,
			Thresholds: thresholds,
			Version:    config.BuildVersion,
			Logger:     logger,
		})
		srv, err := promexporter.Serve(ctx, cfg.PrometheusListen, exp.Handler(), logger)
		if err != nil {
			logger.Error("start prometheus endpoint", "err", err)
			return 2
		}
		defer srv.Close(context.Background())
		policy.sink = exp
		logger.Info("prometheus metrics enabled", "addr", srv.Addr(), "path", "/metrics")
	}

	if !cfg.Daemon {
		code := policy.step(ctx, client, thresholds, cfg, sel, cpuSampleWindow, true)
		policy.flush()
		return code
	}

	return runDaemon(ctx, policy, client, thresholds, cfg, sel)
}

// runDaemon drives repeated checks, rescheduling the next run only after the
// current one finishes so a slow check cannot pile up ticks.
func runDaemon(ctx context.Context, policy *alertPolicy, client *alerter.Client, th monitor.Thresholds, cfg *config.Config, sel monitor.Selection) int {
	code := 0

	// First cycle uses a blocking CPU sample for an accurate reading, then
	// baseline the delta-based sampler used by subsequent ticks.
	start := time.Now()
	code = policy.step(ctx, client, th, cfg, sel, cpuSampleWindow, false)
	logCycle(policy.logger, start)
	monitor.PrimeCPU()

	for {
		select {
		case <-ctx.Done():
			policy.flush()
			policy.logger.Info("shutting down", "reason", ctx.Err())
			return code
		case <-time.After(policy.nextDelay(cfg.Interval, start)):
		}

		start = time.Now()
		if c := policy.step(ctx, client, th, cfg, sel, 0, false); c != 0 {
			code = c
		}
		logCycle(policy.logger, start)
	}
}

// nextDelay returns how long to wait before the next cycle, given when the
// previous cycle started. Overruns are reported and yield a zero delay.
func (p *alertPolicy) nextDelay(interval time.Duration, lastStart time.Time) time.Duration {
	elapsed := time.Since(lastStart)
	if remaining := interval - elapsed; remaining > 0 {
		return remaining
	}
	p.logger.Warn("check overran its interval; starting next cycle immediately",
		"interval", interval, "elapsed", elapsed)
	return 0
}

func logCycle(logger *slog.Logger, start time.Time) {
	logger.Debug("check cycle complete", "duration", time.Since(start))
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case config.LogLevelDebug:
		level = slog.LevelDebug
	case config.LogLevelWarn:
		level = slog.LevelWarn
	case config.LogLevelError:
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == config.LogFormatJSON {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

// openState resolves the state file path and opens it, or returns a nil store
// (persistence disabled) when requested.
func openState(cfg *config.Config, logger *slog.Logger) (*state.Store, error) {
	if cfg.NoState {
		return nil, nil
	}
	path := cfg.StateFile
	if path == "" {
		p, err := state.DefaultPath()
		if err != nil {
			return nil, fmt.Errorf("resolve default state path: %w", err)
		}
		path = p
	}
	return state.Open(path, logger), nil
}

// metricsSink receives the outcome of each collection cycle. A nil sink is a
// no-op, so callers that do not export metrics need not provide one.
type metricsSink interface {
	Observe(monitor.Snapshot, []monitor.Breach)
	ObserveError(error)
}

// alertPolicy decides whether a given cycle of breaches should fire a webhook,
// implementing the always / once / cooldown -alert-mode semantics. State is
// persisted so the decision survives restarts.
type alertPolicy struct {
	mode     config.AlertMode
	cooldown time.Duration
	logger   *slog.Logger
	store    *state.Store
	sink     metricsSink

	lastSent time.Time
	armed    bool
}

func newAlertPolicy(cfg *config.Config, store *state.Store, logger *slog.Logger) *alertPolicy {
	saved := store.Snapshot()
	return &alertPolicy{
		mode:     cfg.AlertMode,
		cooldown: cfg.Cooldown,
		logger:   logger,
		store:    store,
		lastSent: saved.LastSent,
		armed:    saved.Armed,
	}
}

// shouldSend reports whether an alert may fire now, updating policy state and
// persisting any change.
func (p *alertPolicy) shouldSend(breaches []monitor.Breach, now time.Time) bool {
	if len(breaches) == 0 {
		if !p.armed {
			p.armed = true
			p.persist()
		}
		return false
	}
	switch p.mode {
	case config.AlertOnce:
		if !p.armed {
			return false
		}
		p.armed = false
		p.persist()
		return true
	case config.AlertCooldown:
		if !p.lastSent.IsZero() && now.Sub(p.lastSent) < p.cooldown {
			return false
		}
		p.lastSent = now
		p.persist()
		return true
	default: // always
		return true
	}
}

// persist records the current policy state in the store (no-op when disabled).
func (p *alertPolicy) persist() {
	p.store.Set(p.lastSent, p.armed)
}

// flush writes any pending state changes to disk, logging failures.
func (p *alertPolicy) flush() {
	if err := p.store.Save(); err != nil {
		p.logger.Error("save state", "err", err)
	}
}

// step performs one collect/evaluate/alert cycle. sel selects which metrics to
// monitor. cpuWindow is how long cpu.Percent samples (0 = delta since the
// previous call). onExit controls whether breach state contributes to the
// process exit code (-exit-code).
func (p *alertPolicy) step(ctx context.Context, client *alerter.Client, th monitor.Thresholds, cfg *config.Config, sel monitor.Selection, cpuWindow time.Duration, onExit bool) int {
	snap, err := monitor.Collect(cfg.Path, cpuWindow, sel)
	if err != nil {
		p.logger.Error("collect metrics", "err", err)
		if p.sink != nil {
			p.sink.ObserveError(err)
		}
		return 2
	}

	if sel.Process {
		procs, total, err := monitor.TopProcesses(cfg.ProcessCount, monitor.SortKey(cfg.ProcessSort), processSampleWindow)
		if err != nil {
			p.logger.Error("collect processes", "err", err)
		} else {
			snap.Processes = procs
			snap.ProcessCount = total
			if len(procs) > 0 {
				snap.ProcessTopPercent = procs[0].Value(monitor.SortKey(cfg.ProcessSort))
			}
		}
	}

	breaches := monitor.Evaluate(snap, th, sel)
	if p.sink != nil {
		p.sink.Observe(snap, breaches)
	}
	output(cfg, snap, breaches, sel)
	p.logMeasurements(snap, breaches, sel)

	if p.shouldSend(breaches, snap.Timestamp) {
		if cfg.Webhook == "" {
			p.logger.Warn("alert skipped: no webhook URL configured (-webhook)")
		} else if err := client.Send(ctx, breaches, snap); err != nil {
			p.logger.Error("send alert", "err", err)
		} else {
			p.logger.Info("alert delivered", "breaches", len(breaches))
		}
	}

	if onExit && cfg.ExitCode && len(breaches) > 0 {
		return 1
	}
	return 0
}

// logMeasurements emits a one-line per-cycle summary and a structured per-breach
// record, keeping stdout reserved for the -json/-text metrics output.
func (p *alertPolicy) logMeasurements(s monitor.Snapshot, breaches []monitor.Breach, sel monitor.Selection) {
	attrs := []any{"breaches", len(breaches)}
	if sel.Disk {
		attrs = append(attrs, "disk_percent", round2(s.DiskUsedPercent))
	}
	if sel.Mem {
		attrs = append(attrs, "mem_percent", round2(s.MemUsedPercent))
	}
	if sel.CPU {
		attrs = append(attrs, "cpu_percent", round2(s.CPUUsedPercent))
	}
	if sel.Process {
		attrs = append(attrs, "process_count", s.ProcessCount, "process_top_percent", round2(s.ProcessTopPercent))
	}
	p.logger.Info("check complete", attrs...)
	for _, b := range breaches {
		p.logger.Warn("threshold breached",
			"metric", b.Metric, "value", round2(b.Value), "threshold", b.Threshold)
	}
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// cpuSampleWindow is how long cpu.Percent blocks to sample utilization on a
// cold start; later daemon cycles use the delta since the previous tick.
const cpuSampleWindow = time.Second

// processSampleWindow is how long the top-process sampler observes per-process
// CPU usage before ranking.
const processSampleWindow = time.Second

func output(cfg *config.Config, s monitor.Snapshot, breaches []monitor.Breach, sel monitor.Selection) {
	// In daemon mode metrics are logged; stdout is only used for -json so that
	// normal daemon operation stays quiet unless explicitly requested.
	if cfg.QuietOutput() {
		return
	}

	if cfg.JSON {
		enc := json.NewEncoder(os.Stdout)
		if !cfg.Daemon {
			enc.SetIndent("", "  ")
		}
		out := map[string]any{
			"timestamp": s.Timestamp,
		}
		if len(breaches) > 0 {
			out["breaches"] = breaches
		}
		if sel.Disk {
			out["disk_used_percent"] = s.DiskUsedPercent
			out["disk_total_bytes"] = s.DiskTotalBytes
			out["disk_used_bytes"] = s.DiskUsedBytes
		}
		if sel.Mem {
			out["mem_used_percent"] = s.MemUsedPercent
			out["mem_total_bytes"] = s.MemTotalBytes
			out["mem_used_bytes"] = s.MemUsedBytes
		}
		if sel.CPU {
			out["cpu_used_percent"] = s.CPUUsedPercent
		}
		if sel.Process {
			out["process_count"] = s.ProcessCount
			out["process_top_percent"] = s.ProcessTopPercent
			out["processes"] = s.Processes
		}
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "ey: encode output: %v\n", err)
		}
		return
	}

	color := useColor(cfg)
	breached := map[string]bool{}
	for _, b := range breaches {
		breached[b.Metric] = true
	}

	fmt.Printf("%s\n", paint(color, true, "System Status Check - "+s.Timestamp.Format(time.RFC822)))
	if sel.Disk {
		paintValue(color, "Disk Usage", fmt.Sprintf("%.2f%% (%.1f/%.1f GiB) @ %s",
			s.DiskUsedPercent, monitor.GiB(s.DiskUsedBytes), monitor.GiB(s.DiskTotalBytes), cfg.Path), breached["disk"])
	}
	if sel.Mem {
		paintValue(color, "RAM Usage", fmt.Sprintf("%.2f%% (%.1f/%.1f GiB)",
			s.MemUsedPercent, monitor.GiB(s.MemUsedBytes), monitor.GiB(s.MemTotalBytes)), breached["memory"])
	}
	if sel.CPU {
		paintValue(color, "CPU Usage", fmt.Sprintf("%.2f%%", s.CPUUsedPercent), breached["cpu"])
	}
	if sel.Process {
		paintValue(color, "Processes", fmt.Sprintf("%d running", s.ProcessCount), breached["process"] || breached["process_count"])
		if len(s.Processes) > 0 {
			fmt.Printf("Top %d by %s:\n", len(s.Processes), cfg.ProcessSort)
			for _, pr := range s.Processes {
				fmt.Printf("  %-6d %-24.24s %6.1f%% cpu  %6.1f%% mem  %6.1f MiB\n",
					pr.PID, pr.Name, pr.CPUPercent, pr.MemPercent, float64(pr.RSSBytes)/(1<<20))
			}
		}
	}

	if len(breaches) == 0 {
		fmt.Println(paint(color, true, "Status: OK (all thresholds satisfied)"))
		return
	}
	fmt.Println(paint(color, false, "Threshold breaches:"))
	for _, b := range breaches {
		fmt.Printf("  - %s\n", formatBreach(b))
	}
}

// formatBreach renders a breach, using a count format for process_count.
func formatBreach(b monitor.Breach) string {
	if b.Metric == "process_count" {
		return fmt.Sprintf("%s: %.0f > %.0f", b.Metric, b.Value, b.Threshold)
	}
	return fmt.Sprintf("%s: %.2f%% > %.2f%%", b.Metric, b.Value, b.Threshold)
}

// useColor decides whether to emit ANSI color, honoring -color.
func useColor(cfg *config.Config) bool {
	switch cfg.Color {
	case config.ColorAlways:
		return true
	case config.ColorNever:
		return false
	default:
		return isTerminal(os.Stdout)
	}
}

func paintValue(color bool, label, value string, breached bool) {
	if !color {
		fmt.Printf("%s: %s\n", label, value)
		return
	}
	if breached {
		fmt.Printf("%s: %s%s%s\n", label, ansiRed, value, ansiReset)
		return
	}
	fmt.Printf("%s: %s%s%s\n", label, ansiGreen, value, ansiReset)
}

func paint(color, ok bool, s string) string {
	if !color {
		return s
	}
	if ok {
		return ansiGreen + s + ansiReset
	}
	return ansiRed + s + ansiReset
}

const (
	ansiReset = "\x1b[0m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
)

// isTerminal reports whether f is attached to a character device (TTY).
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runConfig implements the "ey config" subcommand: it prints the resolved
// config path (-path), prints the effective configuration (-print), or runs
// the interactive wizard to write one.
func runConfig(args []string) int {
	pfs := flag.NewFlagSet("ey config", flag.ContinueOnError)
	var showPath, printCfg bool
	var configPath string
	pfs.Usage = func() {
		fmt.Fprint(pfs.Output(), "Usage: ey config [flags]\n\nManage ey's configuration file.\n\nFlags:\n")
		pfs.PrintDefaults()
	}
	pfs.BoolVar(&showPath, "path", false, "Print the resolved config file path and exit")
	pfs.BoolVar(&printCfg, "print", false, "Print the effective configuration and exit")
	pfs.StringVar(&configPath, "config", "", "Config file to inspect or manage (default: $EY_CONFIG or the per-user location)")
	if err := pfs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if pfs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "ey config: unexpected argument %q\n", pfs.Arg(0))
		return 2
	}
	if configPath == "" {
		configPath = os.Getenv("EY_CONFIG")
	}
	if configPath == "" {
		p, err := config.DefaultConfigPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ey config: %v\n", err)
			return 1
		}
		configPath = p
	}

	if showPath {
		fmt.Println(configPath)
		return 0
	}

	base, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ey config: %v\n", err)
		return 1
	}

	if printCfg {
		data, err := config.Marshal(base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ey config: %v\n", err)
			return 1
		}
		os.Stdout.Write(data)
		return 0
	}

	if !isTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "ey config: interactive mode requires a terminal; use 'ey config -print' to preview or set -config/EY_CONFIG to point at a file")
		return 2
	}

	result, err := wizard.Run(os.Stdin, os.Stdout, base)
	if errors.Is(err, wizard.ErrAborted) {
		fmt.Fprintln(os.Stdout, "Not saving changes.")
		return 0
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(os.Stderr, "ey config: input ended before all prompts were answered; nothing written")
		} else {
			fmt.Fprintf(os.Stderr, "ey config: %v\n", err)
		}
		return 2
	}
	if err := config.WriteFile(configPath, config.FromConfig(result)); err != nil {
		fmt.Fprintf(os.Stderr, "ey config: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "Configuration written to %s\n", configPath)
	return 0
}
