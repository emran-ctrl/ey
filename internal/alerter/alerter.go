package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ey/internal/config"
	"ey/internal/monitor"
)

// defaultMaxBackoff caps the exponential retry delay.
const defaultMaxBackoff = 8 * time.Second

// Options configures a Client.
type Options struct {
	Format     config.Format
	Timeout    time.Duration
	Retries    int
	Backoff    time.Duration
	MaxBackoff time.Duration
	Logger     *slog.Logger
}

// Client delivers threshold-breach alerts to an external webhook.
type Client struct {
	url        string
	format     config.Format
	http       *http.Client
	timeout    time.Duration
	retries    int
	backoff    time.Duration
	maxBackoff time.Duration
	logger     *slog.Logger
}

// New builds a Client. Format is resolved from auto-detection when set to
// config.FormatAuto. Zero-valued Options fields fall back to sensible defaults.
func New(webhook string, opts Options) *Client {
	format := opts.Format
	if format == config.FormatAuto {
		format = detect(webhook)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.Retries < 0 {
		opts.Retries = 0
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 500 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = defaultMaxBackoff
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Client{
		url:        webhook,
		format:     format,
		http:       &http.Client{Timeout: opts.Timeout},
		timeout:    opts.Timeout,
		retries:    opts.Retries,
		backoff:    opts.Backoff,
		maxBackoff: opts.MaxBackoff,
		logger:     opts.Logger,
	}
}

// detect guesses the webhook format from the URL host.
func detect(webhook string) config.Format {
	u, err := url.Parse(webhook)
	if err != nil {
		return config.FormatGeneric
	}
	host := strings.ToLower(u.Host)
	switch {
	case strings.Contains(host, "slack.com"):
		return config.FormatSlack
	case strings.Contains(host, "discord.com") || strings.Contains(host, "discordapp.com"):
		return config.FormatDiscord
	default:
		return config.FormatGeneric
	}
}

// payload builds the request body for the configured format. format is always
// known here because New resolves auto via detection at construction time.
func (c *Client) payload(breaches []monitor.Breach, s monitor.Snapshot) any {
	switch c.format {
	case config.FormatSlack:
		return map[string]string{"text": message(breaches, s)}
	case config.FormatDiscord:
		return map[string]string{"content": message(breaches, s)}
	default:
		return struct {
			Snapshot monitor.Snapshot `json:"snapshot"`
			Breaches []monitor.Breach `json:"breaches"`
		}{Snapshot: s, Breaches: breaches}
	}
}

// message renders a human-readable alert line covering every breach.
func message(breaches []monitor.Breach, s monitor.Snapshot) string {
	var b strings.Builder
	b.WriteString(":rotating_light: *System alert* — ")
	for i, br := range breaches {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s at %.2f%% (threshold %.2f%%)", br.Metric, br.Value, br.Threshold)
	}
	fmt.Fprintf(&b, " | disk %.2f%% (%.1f/%.1f GiB), mem %.2f%%, cpu %.2f%%",
		s.DiskUsedPercent, monitor.GiB(s.DiskUsedBytes), monitor.GiB(s.DiskTotalBytes),
		s.MemUsedPercent, s.CPUUsedPercent)
	return b.String()
}

// Send posts the alert payload to the webhook, retrying transport failures,
// 429s, and 5xx responses with exponential backoff and full jitter. Other 4xx
// responses are returned immediately. Each attempt is bounded by the client
// timeout; cancellation of ctx aborts both requests and backoff waits.
func (c *Client) Send(ctx context.Context, breaches []monitor.Breach, s monitor.Snapshot) error {
	if c.url == "" {
		return fmt.Errorf("no webhook URL configured")
	}
	body, err := json.Marshal(c.payload(breaches, s))
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			delay := c.backoffDelay(attempt, lastRetryAfter(lastErr))
			c.logger.Warn("retrying webhook delivery",
				"attempt", attempt+1, "of", c.retries+1, "retry_in", delay, "err", lastErr)
			if err := sleep(ctx, delay); err != nil {
				return fmt.Errorf("retry aborted: %w", err)
			}
		}

		retryable, retryAfter, err := c.attempt(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = &retryError{err: err, retryAfter: retryAfter}

		if !retryable {
			return err
		}
		if attempt == c.retries {
			if c.retries == 0 {
				return err
			}
			return fmt.Errorf("giving up after %d attempts: %w", c.retries+1, err)
		}
	}
	return lastErr
}

// attempt performs a single POST. It reports whether the failure is retryable
// and any Retry-After hint from the server.
func (c *Client) attempt(ctx context.Context, body []byte) (bool, time.Duration, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return false, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return true, 0, fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		io.Copy(io.Discard, resp.Body)
		return false, 0, nil
	}

	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	err = fmt.Errorf("webhook returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))

	retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	return retryable, parseRetryAfter(resp.Header.Get("Retry-After")), err
}

// retryError carries a server Retry-After hint alongside the error.
type retryError struct {
	err        error
	retryAfter time.Duration
}

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

func lastRetryAfter(err error) time.Duration {
	var re *retryError
	if errors.As(err, &re) {
		return re.retryAfter
	}
	return 0
}

// backoffDelay returns the wait before retry number attempt (1-based). It uses
// exponential backoff with full jitter, never exceeding maxBackoff. A server
// Retry-After hint is honored as a lower bound (also capped at maxBackoff).
func (c *Client) backoffDelay(attempt int, retryAfter time.Duration) time.Duration {
	base := c.backoff
	for i := 1; i < attempt; i++ {
		base *= 2
		if base >= c.maxBackoff {
			break
		}
	}
	if base > c.maxBackoff || base <= 0 {
		base = c.maxBackoff
	}

	delay := time.Duration(rand.Int64N(int64(base) + 1))

	if retryAfter > c.maxBackoff {
		retryAfter = c.maxBackoff
	}
	if retryAfter > delay {
		return retryAfter
	}
	return delay
}

// parseRetryAfter interprets a Retry-After header as either delay-seconds or an
// HTTP date.
func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
