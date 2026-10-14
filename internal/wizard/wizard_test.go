package wizard

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"ey/internal/config"
)

func baseConfig() *config.Config {
	return &config.Config{
		DiskThreshold: 80,
		MemThreshold:  90,
		CPUThreshold:  90,
		Path:          "/",
		Format:        config.FormatAuto,
		Interval:      60 * time.Second,
		AlertMode:     config.AlertAlways,
		Cooldown:      10 * time.Minute,
		Timeout:       5 * time.Second,
		LogFormat:     config.LogFormatText,
		LogLevel:      config.LogLevelInfo,
		Retries:       3,
		RetryBackoff:  500 * time.Millisecond,
		Metrics:       "disk,memory,cpu",
		Color:         config.ColorAuto,

		ProcessCount:          5,
		ProcessSort:           config.ProcessSortCPU,
		ProcessThreshold:      90,
		ProcessCountThreshold: 0,
	}
}

func runScript(t *testing.T, base *config.Config, lines []string) (*config.Config, error) {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	return Run(in, &out, base)
}

func TestRunKeepsDefaults(t *testing.T) {
	base := baseConfig()
	lines := make([]string, 27)
	got, err := runScript(t, base, lines)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(got, base) {
		t.Errorf("Run with all-default answers changed the config:\n got: %+v\nwant: %+v", got, base)
	}
}

func TestRunOverridesValues(t *testing.T) {
	lines := []string{
		"95",       // disk threshold
		"", "", "", // mem, cpu, path
		"cpu,disk",                             // metrics
		"",                                     // process count
		"",                                     // process sort
		"",                                     // process threshold
		"",                                     // process count threshold
		"discord",                              // format
		"https://hooks.slack.com/services/abc", // webhook
		"once",                                 // alert mode
		"1m",                                   // cooldown
		"1",                                    // retries
		"1s",                                   // retry backoff
		"2s",                                   // timeout
		"yes",                                  // json
		"never",                                // color
		"json",                                 // log format
		"debug",                                // log level
		"no",                                   // quiet
		"no",                                   // daemon
		"10s",                                  // interval
		"yes",                                  // exit code
		"",                                     // state file
		"no",                                   // no state
		"y",                                    // write
	}
	got, err := runScript(t, baseConfig(), lines)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := baseConfig()
	want.DiskThreshold = 95
	want.Metrics = "disk,cpu"
	want.Format = config.FormatDiscord
	want.Webhook = "https://hooks.slack.com/services/abc"
	want.AlertMode = config.AlertOnce
	want.Cooldown = time.Minute
	want.Retries = 1
	want.RetryBackoff = time.Second
	want.Timeout = 2 * time.Second
	want.JSON = true
	want.Color = config.ColorNever
	want.LogFormat = config.LogFormatJSON
	want.LogLevel = config.LogLevelDebug
	want.Quiet = false
	want.Daemon = false
	want.Interval = 10 * time.Second
	want.ExitCode = true
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Run result mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestRunRePromptsUntilValid(t *testing.T) {
	lines := []string{
		"101",   // invalid disk threshold -> reprompt
		"50",    // valid disk threshold
		"",      // mem
		"",      // cpu
		"",      // path
		"",      // metrics
		"",      // process count
		"",      // process sort
		"",      // process threshold
		"",      // process count threshold
		"xml",   // invalid format -> reprompt
		"slack", // valid format
		"",      // webhook
		"",      // alert mode
		"",      // cooldown
		"",      // retries
		"",      // retry backoff
		"",      // timeout
		"",      // json
		"",      // color
		"",      // log format
		"",      // log level
		"",      // quiet
		"",      // daemon
		"",      // interval
		"",      // exit code
		"",      // state file
		"",      // no state
		"y",     // write
	}
	want := baseConfig()
	want.DiskThreshold = 50
	want.Format = config.FormatSlack
	got, err := runScript(t, baseConfig(), lines)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Run result mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestRunAbortsWhenDeclined(t *testing.T) {
	lines := make([]string, 26)
	lines = append(lines, "n")
	_, err := runScript(t, baseConfig(), lines)
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("Run = %v, want ErrAborted", err)
	}
}

func TestRunRejectsBadWebhookAndReprompts(t *testing.T) {
	lines := []string{
		"", "", "", "", // disk, memory, cpu, path
		"",             // metrics
		"", "", "", "", // process count, sort, threshold, count threshold
		"",                                                         // format
		"ftp://bad",                                                // invalid webhook -> reprompt
		"",                                                         // valid webhook (keep default = empty)
		"", "", "", "", "", "", "", "", "", "", "", "", "", "", "", // remaining prompts (alert mode .. no state)
		"y", // write
	}
	got, err := runScript(t, baseConfig(), lines)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(got, baseConfig()) {
		t.Errorf("Run result mismatch:\n got: %+v\nwant: %+v", got, baseConfig())
	}
}
