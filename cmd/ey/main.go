package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ey/internal/alerter"
	"ey/internal/config"
	"ey/internal/monitor"
	"ey/internal/state"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
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
		Disk: cfg.DiskThreshold,
		Mem:  cfg.MemThreshold,
		CPU:  cfg.CPUThreshold,
	}

	if !cfg.Daemon {
		code := policy.step(ctx, client, thresholds, cfg, cpuSampleWindow, true)
		policy.flush()
		return code
	}

	return runDaemon(ctx, policy, client, thresholds, cfg)
}

// runDaemon drives repeated checks, rescheduling the next run only after the
// current one finishes so a slow check cannot pile up ticks.
func runDaemon(ctx context.Context, policy *alertPolicy, client *alerter.Client, th monitor.Thresholds, cfg *config.Config) int {
	code := 0

	// First cycle uses a blocking CPU sample for an accurate reading, then
	// baseline the delta-based sampler used by subsequent ticks.
	start := time.Now()
	code = policy.step(ctx, client, th, cfg, cpuSampleWindow, false)
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
		if c := policy.step(ctx, client, th, cfg, 0, false); c != 0 {
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

// alertPolicy decides whether a given cycle of breaches should fire a webhook,
// implementing the always / once / cooldown -alert-mode semantics. State is
// persisted so the decision survives restarts.
type alertPolicy struct {
	mode     config.AlertMode
	cooldown time.Duration
	logger   *slog.Logger
	store    *state.Store

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

// step performs one collect/evaluate/alert cycle. cpuWindow is how long
// cpu.Percent samples (0 = delta since the previous call). onExit controls
// whether breach state contributes to the process exit code (-exit-code).
func (p *alertPolicy) step(ctx context.Context, client *alerter.Client, th monitor.Thresholds, cfg *config.Config, cpuWindow time.Duration, onExit bool) int {
	snap, err := monitor.Collect(cfg.Path, cpuWindow)
	if err != nil {
		p.logger.Error("collect metrics", "err", err)
		return 2
	}

	breaches := monitor.Evaluate(snap, th)
	output(cfg, snap, breaches)
	p.logMeasurements(snap, breaches)

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
func (p *alertPolicy) logMeasurements(s monitor.Snapshot, breaches []monitor.Breach) {
	p.logger.Info("check complete",
		"disk_percent", round2(s.DiskUsedPercent),
		"mem_percent", round2(s.MemUsedPercent),
		"cpu_percent", round2(s.CPUUsedPercent),
		"breaches", len(breaches),
	)
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

func output(cfg *config.Config, s monitor.Snapshot, breaches []monitor.Breach) {
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
		if err := enc.Encode(struct {
			monitor.Snapshot
			Breaches []monitor.Breach `json:"breaches,omitempty"`
		}{Snapshot: s, Breaches: breaches}); err != nil {
			fmt.Fprintf(os.Stderr, "ey: encode output: %v\n", err)
		}
		return
	}

	fmt.Printf("System Status Check - %s\n", s.Timestamp.Format(time.RFC822))
	fmt.Printf("Disk Usage: %.2f%% (%.1f/%.1f GiB) @ %s\n",
		s.DiskUsedPercent, monitor.GiB(s.DiskUsedBytes), monitor.GiB(s.DiskTotalBytes), cfg.Path)
	fmt.Printf("RAM Usage:  %.2f%% (%.1f/%.1f GiB)\n",
		s.MemUsedPercent, monitor.GiB(s.MemUsedBytes), monitor.GiB(s.MemTotalBytes))
	fmt.Printf("CPU Usage:  %.2f%%\n", s.CPUUsedPercent)

	if len(breaches) == 0 {
		fmt.Println("Status: OK (all thresholds satisfied)")
		return
	}
	fmt.Println("Threshold breaches:")
	for _, b := range breaches {
		fmt.Printf("  - %s: %.2f%% > %.2f%%\n", b.Metric, b.Value, b.Threshold)
	}
}
