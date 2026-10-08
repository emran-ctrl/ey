package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ey/internal/alerter"
	"ey/internal/config"
	"ey/internal/monitor"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := alerter.New(cfg.Webhook, cfg.Format, cfg.Timeout)
	policy := newAlertPolicy(cfg)

	thresholds := monitor.Thresholds{
		Disk: cfg.DiskThreshold,
		Mem:  cfg.MemThreshold,
		CPU:  cfg.CPUThreshold,
	}

	if !cfg.Daemon {
		return policy.step(ctx, client, thresholds, cfg, cpuSampleWindow, true)
	}

	// First cycle uses a blocking CPU sample for an accurate reading, then
	// baseline the delta-based sampler used by subsequent ticks.
	code := policy.step(ctx, client, thresholds, cfg, cpuSampleWindow, false)
	monitor.PrimeCPU()

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return code
		case <-ticker.C:
			if c := policy.step(ctx, client, thresholds, cfg, 0, false); c != 0 {
				code = c
			}
		}
	}
}

// alertPolicy decides whether a given cycle of breaches should fire a webhook,
// implementing the always / once / cooldown -alert-mode semantics.
type alertPolicy struct {
	mode     config.AlertMode
	cooldown time.Duration
	lastSent time.Time
	armed    bool // for "once": true until a breach has been alerted, re-armed on recovery
}

func newAlertPolicy(cfg *config.Config) *alertPolicy {
	return &alertPolicy{
		mode:     cfg.AlertMode,
		cooldown: cfg.Cooldown,
		armed:    true,
	}
}

// shouldSend reports whether an alert may fire now, updating policy state.
// It is called once per check cycle with the full breach list.
func (p *alertPolicy) shouldSend(breaches []monitor.Breach, now time.Time) bool {
	if len(breaches) == 0 {
		p.armed = true // recovery re-arms "once" mode
		return false
	}
	switch p.mode {
	case config.AlertOnce:
		if !p.armed {
			return false
		}
		p.armed = false
		return true
	case config.AlertCooldown:
		if !p.lastSent.IsZero() && now.Sub(p.lastSent) < p.cooldown {
			return false
		}
		p.lastSent = now
		return true
	default: // always
		return true
	}
}

// step performs one collect/evaluate/alert cycle. cpuWindow is how long
// cpu.Percent samples (0 = delta since the previous call). onExit controls
// whether breach state contributes to the process exit code (-exit-code).
func (p *alertPolicy) step(ctx context.Context, client *alerter.Client, th monitor.Thresholds, cfg *config.Config, cpuWindow time.Duration, onExit bool) int {
	snap, err := monitor.Collect(cfg.Path, cpuWindow)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ey: collect: %v\n", err)
		return 2
	}

	breaches := monitor.Evaluate(snap, th)
	output(cfg, snap, breaches)

	if p.shouldSend(breaches, snap.Timestamp) {
		if cfg.Webhook == "" {
			fmt.Fprintln(os.Stderr, "ey: alert skipped: no webhook URL provided (-webhook)")
		} else if err := client.Send(ctx, breaches, snap); err != nil {
			fmt.Fprintf(os.Stderr, "ey: send alert: %v\n", err)
		} else {
			fmt.Println("alert sent")
		}
	}

	if onExit && cfg.ExitCode && len(breaches) > 0 {
		return 1
	}
	return 0
}

// cpuSampleWindow is how long cpu.Percent blocks to sample utilization on a
// cold start; later daemon cycles use the delta since the previous tick.
const cpuSampleWindow = time.Second

func output(cfg *config.Config, s monitor.Snapshot, breaches []monitor.Breach) {
	if cfg.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
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
