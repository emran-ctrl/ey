// Package promexporter exposes ey's collected metrics in Prometheus text
// format over HTTP. It owns a private registry (no global state) and is
// deliberately decoupled from the rest of ey: callers feed it monitor
// snapshots and breaches, and it knows nothing about flags or alerting.
package promexporter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ey/internal/monitor"
)

// namespace prefixes every metric family exposed by ey.
const namespace = "ey"

// Options configures a new Exporter.
type Options struct {
	// Selection decides which metric families are registered and reported.
	Selection monitor.Selection
	// Thresholds are reported as gauge values for the selected metrics.
	Thresholds monitor.Thresholds
	// Version is exposed via the ey_build_info constant label.
	Version string
	// Logger receives operational messages; nil falls back to slog.Default.
	Logger *slog.Logger
}

// Exporter holds a private registry and the ey_* families for the selected
// resources. It is safe for concurrent use: Observe is called from a single
// collection goroutine while the HTTP handler scrapes concurrently.
type Exporter struct {
	reg      *prometheus.Registry
	selected map[string]bool

	diskUsedPercent prometheus.Gauge
	diskTotalBytes  prometheus.Gauge
	diskUsedBytes   prometheus.Gauge

	memUsedPercent prometheus.Gauge
	memTotalBytes  prometheus.Gauge
	memUsedBytes   prometheus.Gauge

	cpuUsedPercent prometheus.Gauge

	thresholdPercent  *prometheus.GaugeVec
	thresholdBreached *prometheus.GaugeVec

	lastCollect   prometheus.Gauge
	collectErrors prometheus.Counter
}

// New builds an Exporter with a private registry populated only with the
// selected metric families.
func New(opts Options) *Exporter {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	e := &Exporter{
		reg:      prometheus.NewRegistry(),
		selected: map[string]bool{},
	}
	logger.Debug("prometheus exporter initialized")

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   namespace,
		Name:        "build_info",
		Help:        "Build information; the value is always 1.",
		ConstLabels: prometheus.Labels{"version": opts.Version},
	})
	buildInfo.Set(1)
	e.reg.MustRegister(buildInfo)

	e.thresholdPercent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "threshold_percent",
		Help:      "Configured threshold percentage for the metric.",
	}, []string{"metric"})
	e.reg.MustRegister(e.thresholdPercent)

	e.thresholdBreached = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "threshold_breached",
		Help:      "1 when the metric exceeded its threshold in the last check, 0 otherwise.",
	}, []string{"metric"})
	e.reg.MustRegister(e.thresholdBreached)

	e.lastCollect = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "last_collect_timestamp_seconds",
		Help:      "Unix timestamp of the last successful collection.",
	})
	e.reg.MustRegister(e.lastCollect)

	e.collectErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "collect_errors_total",
		Help:      "Total number of failed metric collections.",
	})
	e.reg.MustRegister(e.collectErrors)

	thresholds := map[string]float64{}
	if opts.Selection.Disk {
		e.diskUsedPercent = e.gauge("disk_used_percent", "Disk space used, as a percentage.")
		e.diskTotalBytes = e.gauge("disk_total_bytes", "Total disk space in bytes.")
		e.diskUsedBytes = e.gauge("disk_used_bytes", "Used disk space in bytes.")
		thresholds["disk"] = opts.Thresholds.Disk
	}
	if opts.Selection.Mem {
		e.memUsedPercent = e.gauge("memory_used_percent", "Memory used, as a percentage.")
		e.memTotalBytes = e.gauge("memory_total_bytes", "Total memory in bytes.")
		e.memUsedBytes = e.gauge("memory_used_bytes", "Used memory in bytes.")
		thresholds["memory"] = opts.Thresholds.Mem
	}
	if opts.Selection.CPU {
		e.cpuUsedPercent = e.gauge("cpu_used_percent", "CPU used, as a percentage.")
		thresholds["cpu"] = opts.Thresholds.CPU
	}

	for name, val := range thresholds {
		e.selected[name] = true
		e.thresholdPercent.WithLabelValues(name).Set(val)
		e.thresholdBreached.WithLabelValues(name).Set(0)
	}

	return e
}

// gauge registers and returns a new gauge in the exporter's namespace.
func (e *Exporter) gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Namespace: namespace, Name: name, Help: help})
	e.reg.MustRegister(g)
	return g
}

// Observe records one successful collection cycle.
func (e *Exporter) Observe(s monitor.Snapshot, breaches []monitor.Breach) {
	if e.diskUsedPercent != nil {
		e.diskUsedPercent.Set(s.DiskUsedPercent)
		e.diskTotalBytes.Set(float64(s.DiskTotalBytes))
		e.diskUsedBytes.Set(float64(s.DiskUsedBytes))
	}
	if e.memUsedPercent != nil {
		e.memUsedPercent.Set(s.MemUsedPercent)
		e.memTotalBytes.Set(float64(s.MemTotalBytes))
		e.memUsedBytes.Set(float64(s.MemUsedBytes))
	}
	if e.cpuUsedPercent != nil {
		e.cpuUsedPercent.Set(s.CPUUsedPercent)
	}

	breached := make(map[string]float64, len(e.selected))
	for name := range e.selected {
		breached[name] = 0
	}
	for _, b := range breaches {
		if _, ok := e.selected[b.Metric]; ok {
			breached[b.Metric] = 1
		}
	}
	for name, v := range breached {
		e.thresholdBreached.WithLabelValues(name).Set(v)
	}

	e.lastCollect.Set(float64(s.Timestamp.Unix()))
}

// ObserveError records a failed collection cycle without disturbing the last
// known-good gauge values, so a scraper can detect staleness.
func (e *Exporter) ObserveError(error) {
	e.collectErrors.Inc()
}

// Handler returns the HTTP handler that serves the registry. Only GET
// /metrics is routed; every other path returns 404.
func (e *Exporter) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(e.reg, promhttp.HandlerOpts{}))
	return mux
}

// Server is a running metrics HTTP endpoint.
type Server struct {
	ln     net.Listener
	srv    *http.Server
	logger *slog.Logger
}

// Serve binds addr synchronously (so callers receive bind errors) and then
// serves h in the background, shutting down gracefully when ctx is done.
func Serve(ctx context.Context, addr string, h http.Handler, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	s := &Server{ln: ln, srv: srv, logger: logger}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("prometheus server shutdown", "err", err)
		}
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("prometheus server stopped", "err", err)
		}
	}()

	return s, nil
}

// Addr returns the bound listener address, useful when serving on port 0.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close shuts the server down, waiting for in-flight requests up to ctx.
func (s *Server) Close(ctx context.Context) error { return s.srv.Shutdown(ctx) }
