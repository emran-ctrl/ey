package alerter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ey/internal/config"
	"ey/internal/monitor"
)

var (
	testSnap = monitor.Snapshot{
		Timestamp:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		DiskUsedPercent: 91.5,
		DiskTotalBytes:  100 << 30,
		DiskUsedBytes:   915 << 20,
		MemUsedPercent:  55,
		MemTotalBytes:   16 << 30,
		MemUsedBytes:    88 << 20,
		CPUUsedPercent:  12.5,
	}
	testBreaches = []monitor.Breach{
		{Metric: "disk", Value: 91.5, Threshold: 80},
	}
)

func TestDetect(t *testing.T) {
	tests := []struct {
		url  string
		want config.Format
	}{
		{"https://hooks.slack.com/services/T00/B00/xxx", config.FormatSlack},
		{"https://discord.com/api/webhooks/1/token", config.FormatDiscord},
		{"https://discordapp.com/api/webhooks/1/token", config.FormatDiscord},
		{"https://example.com/hook", config.FormatGeneric},
		{"not a url", config.FormatGeneric},
	}
	for _, tt := range tests {
		if got := detect(tt.url); got != tt.want {
			t.Errorf("detect(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestNewResolvesAuto(t *testing.T) {
	c := New("https://hooks.slack.com/x", config.FormatAuto, time.Second)
	if c.format != config.FormatSlack {
		t.Errorf("format = %q, want slack", c.format)
	}
	c = New("https://example.com/x", config.FormatAuto, time.Second)
	if c.format != config.FormatGeneric {
		t.Errorf("format = %q, want generic", c.format)
	}
}

func TestSendPayloadShapes(t *testing.T) {
	tests := []struct {
		name       string
		format     config.Format
		assertBody func(t *testing.T, body []byte)
	}{
		{
			name:   "slack",
			format: config.FormatSlack,
			assertBody: func(t *testing.T, body []byte) {
				var p map[string]string
				if err := json.Unmarshal(body, &p); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				text, ok := p["text"]
				if !ok {
					t.Fatal(`missing "text" key`)
				}
				for _, want := range []string{"disk at 91.50%", "threshold 80.00%"} {
					if !strings.Contains(text, want) {
						t.Errorf("text missing %q: %s", want, text)
					}
				}
			},
		},
		{
			name:   "discord",
			format: config.FormatDiscord,
			assertBody: func(t *testing.T, body []byte) {
				var p map[string]string
				if err := json.Unmarshal(body, &p); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if _, ok := p["content"]; !ok {
					t.Error(`missing "content" key`)
				}
				if _, ok := p["text"]; ok {
					t.Error(`discord payload must not have "text" key`)
				}
			},
		},
		{
			name:   "generic",
			format: config.FormatGeneric,
			assertBody: func(t *testing.T, body []byte) {
				var p struct {
					Snapshot monitor.Snapshot `json:"snapshot"`
					Breaches []monitor.Breach `json:"breaches"`
				}
				if err := json.Unmarshal(body, &p); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if p.Snapshot.DiskUsedPercent != 91.5 {
					t.Errorf("snapshot.disk_used_percent = %g, want 91.5", p.Snapshot.DiskUsedPercent)
				}
				if len(p.Breaches) != 1 || p.Breaches[0].Metric != "disk" {
					t.Errorf("breaches = %+v, want one disk breach", p.Breaches)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			var gotCT string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotCT = r.Header.Get("Content-Type")
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			c := New(srv.URL, tt.format, time.Second)
			if err := c.Send(context.Background(), testBreaches, testSnap); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if gotCT != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", gotCT)
			}
			tt.assertBody(t, gotBody)
		})
	}
}

func TestSendAccepts204(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, config.FormatDiscord, time.Second)
	if err := c.Send(context.Background(), testBreaches, testSnap); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSendNon2xxReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "channel_not_found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, config.FormatSlack, time.Second)
	err := c.Send(context.Background(), testBreaches, testSnap)
	if err == nil {
		t.Fatal("Send = nil error, want error")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("error %q missing status or body", err)
	}
}

func TestSendNoWebhook(t *testing.T) {
	c := New("", config.FormatAuto, time.Second)
	if err := c.Send(context.Background(), testBreaches, testSnap); err == nil {
		t.Fatal("Send = nil error, want error for empty URL")
	}
}

func TestSendTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := New(srv.URL, config.FormatSlack, 20*time.Millisecond)
	if err := c.Send(context.Background(), testBreaches, testSnap); err == nil {
		t.Fatal("Send = nil error, want timeout error")
	}
}
