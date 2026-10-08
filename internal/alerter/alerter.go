package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ey/internal/config"
	"ey/internal/monitor"
)

// Client delivers threshold-breach alerts to an external webhook.
type Client struct {
	url    string
	format config.Format
	http   *http.Client
}

// New builds a Client. format is resolved from auto-detection when set to
// config.FormatAuto.
func New(webhook string, format config.Format, timeout time.Duration) *Client {
	if format == config.FormatAuto {
		format = detect(webhook)
	}
	return &Client{
		url:    webhook,
		format: format,
		http:   &http.Client{Timeout: timeout},
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

// Send posts the alert payload to the webhook. It returns an error for
// transport failures and non-2xx responses.
func (c *Client) Send(ctx context.Context, breaches []monitor.Breach, s monitor.Snapshot) error {
	if c.url == "" {
		return fmt.Errorf("no webhook URL configured")
	}
	body, err := json.Marshal(c.payload(breaches, s))
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
