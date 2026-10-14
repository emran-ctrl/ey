# ey

[![CI](https://github.com/emran-ctrl/ey/actions/workflows/ci.yml/badge.svg)](https://github.com/emran-ctrl/ey/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/emran-ctrl/ey)](https://github.com/emran-ctrl/ey/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A small CLI system monitor written in Go. `ey` samples disk, memory, CPU, and
top-process usage, compares them against configurable thresholds, and fires a
webhook alert (Slack, Discord, or a generic JSON endpoint) when a limit is
exceeded. It runs either as a one-shot check or as a long-lived daemon.

- Single static binary — statically linked, no external services or runtime
  dependencies
- One-shot or daemon mode
- Slack / Discord / generic webhook payloads with automatic detection
- Alert policies: `always`, `once`, or `cooldown` (persisted across restarts)
- Robust webhook delivery: retries on transport errors, `429`, and `5xx`
  with exponential backoff, full jitter, and `Retry-After` support
- Prometheus metrics endpoint (`-prometheus-listen`) in daemon mode
- Structured logging (`text` or `json`) to stderr, machine-readable metrics to
  stdout (`-json`)
- Interactive configuration wizard (`ey config`) plus a YAML config file
- Pick which metrics to collect (`-metrics`), colorize output (`-color`)
- Top-process tracking (`-metrics process`) with CPU/memory ranking and alerts
- Docker image and hardened systemd units included

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Usage](#usage)
- [Configuration](#configuration)
  - [Flags and environment variables](#flags-and-environment-variables)
  - [Config file](#config-file)
  - [Interactive configuration](#interactive-configuration)
  - [Alert formats](#alert-formats)
  - [Alert modes](#alert-modes)
  - [State persistence](#state-persistence)
  - [Webhook retries](#webhook-retries)
  - [Prometheus metrics](#prometheus-metrics)
- [Deployment](#deployment)
  - [Docker](#docker)
  - [systemd](#systemd)
- [Output](#output)
  - [Top processes](#top-processes)
- [Exit codes](#exit-codes)
- [Building from source](#building-from-source)
- [Development](#development)
- [Releasing](#releasing)
- [Project layout](#project-layout)

## Install

**Prebuilt binaries** — download the archive for your platform from the
[releases page](https://github.com/emran-ctrl/ey/releases) and extract `ey`:

```console
tar -xzf ey_<version>_linux_amd64.tar.gz
install -m 0755 ey /usr/local/bin/ey
```

**Docker** (multi-arch image on GHCR):

```console
docker pull ghcr.io/emran-ctrl/ey:latest
```

**From source** — requires Go 1.27:

```console
go install ./cmd/ey        # or: make build
```

## Quick start

Run a single check against `/` with the default thresholds (disk 80%, memory
90%, CPU 90%):

```console
ey
```

```text
System Status Check - 09 Oct 26 15:02 EAT
Disk Usage: 43.19% (202.0/474.9 GiB) @ /
RAM Usage:  68.65% (10.5/15.3 GiB)
CPU Usage:  3.38%
Status: OK (all thresholds satisfied)
```

Send an alert to Slack when any threshold is breached:

```console
ey -webhook 'https://hooks.slack.com/services/XXX/YYY/ZZZ'
```

Run continuously, checking every 30 seconds, alerting at most once per hour:

```console
ey -daemon -interval 30s -alert-mode cooldown -cooldown 1h \
   -webhook 'https://hooks.slack.com/services/XXX/YYY/ZZZ'
```

## Usage

```text
ey [flags]
```

Common invocations:

```console
# Emit metrics as JSON (handy for scripts and piping into jq).
ey -json | jq .disk_used_percent

# Fail the process when a threshold is breached (for cron / CI / systemd).
ey -exit-code

# Watch /data instead of /, warn at 90% disk.
ey -path /data -disk-threshold 90

# Daemon with JSON logs and a generic webhook.
ey -daemon -log-format json -format generic -webhook https://example.com/hook

# Configure ey interactively (writes ~/.config/ey/config.yaml).
ey config

# Only watch CPU, no color when piped.
ey -metrics cpu -color never

# Showing top processes implies the process metric, so -metrics is optional.
ey -process-count 8 -process-sort memory -process-threshold 80

# Daemon mode with a Prometheus scrape endpoint.
ey -daemon -prometheus-listen :9101
```

Run `ey -h` to print the built-in flag help and `ey config -h` for the
config subcommand's flags.

## Configuration

Precedence is **flag > environment variable > config file > built-in default**.
Every flag has an `EY_*` environment variable equivalent (except `-version`),
which is useful for containers and systemd `EnvironmentFile`s. A config file is
loaded from `-config`, else `$EY_CONFIG`, else the per-user default
`~/.config/ey/config.yaml` (via `XDG_CONFIG_HOME`). A missing config file is
simply ignored; a malformed one is a hard error.

### Flags and environment variables

| Flag | Environment | Default | Description |
| --- | --- | --- | --- |
| `-disk-threshold` | `EY_DISK_THRESHOLD` | `80` | Disk usage percent that triggers an alert (0–100). |
| `-mem-threshold` | `EY_MEM_THRESHOLD` | `90` | Memory usage percent that triggers an alert (0–100). |
| `-cpu-threshold` | `EY_CPU_THRESHOLD` | `90` | CPU usage percent that triggers an alert (0–100). |
| `-path` | `EY_PATH` | `/` | Filesystem path whose usage is checked. |
| `-metrics` | `EY_METRICS` | `disk,memory,cpu` | Comma-separated metrics to monitor (`disk`, `memory`, `cpu`, `process`). |
| `-process-count` | `EY_PROCESS_COUNT` | `5` | Number of top processes to display; implies the `process` metric. |
| `-process-sort` | `EY_PROCESS_SORT` | `cpu` | Rank top processes by `cpu` or `memory`. |
| `-process-threshold` | `EY_PROCESS_THRESHOLD` | `90` | Alert when the top process's usage percent (in the sort dimension) exceeds this. |
| `-process-count-threshold` | `EY_PROCESS_COUNT_THRESHOLD` | `0` | Alert when the total process count exceeds this (`0` disables). |
| `-webhook` | `EY_WEBHOOK` | *(none)* | Slack, Discord, or generic webhook URL. |
| `-format` | `EY_FORMAT` | `auto` | Payload format: `auto`, `slack`, `discord`, or `generic`. |
| `-daemon` | `EY_DAEMON` | `false` | Run continuously instead of a single check. |
| `-interval` | `EY_INTERVAL` | `60s` | Delay between checks in daemon mode. |
| `-alert-mode` | `EY_ALERT_MODE` | `always` | Alert policy: `always`, `once`, or `cooldown`. |
| `-cooldown` | `EY_COOLDOWN` | `10m` | Minimum time between alerts in `cooldown` mode. |
| `-timeout` | `EY_TIMEOUT` | `5s` | HTTP client timeout for webhook delivery. |
| `-json` | `EY_JSON` | `false` | Emit metrics as JSON to stdout. |
| `-color` | `EY_COLOR` | `auto` | Colorize text output: `auto`, `always`, or `never`. |
| `-exit-code` | `EY_EXIT_CODE` | `false` | Exit `1` when any threshold is breached. |
| `-log-format` | `EY_LOG_FORMAT` | `text` | Log output format: `text` or `json`. |
| `-log-level` | `EY_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, or `error`. |
| `-quiet` | `EY_QUIET` | `false` | Suppress the metrics block (logs still go to stderr). |
| `-retries` | `EY_RETRIES` | `3` | Retries after the first webhook attempt (`0` disables). |
| `-retry-backoff` | `EY_RETRY_BACKOFF` | `500ms` | Base delay for retry backoff. |
| `-state-file` | `EY_STATE_FILE` | *(per-user state dir)* | Override the alert state file path. |
| `-no-state` | `EY_NO_STATE` | `false` | Disable persisting alert state to disk. |
| `-prometheus-listen` | `EY_PROMETHEUS_LISTEN` | *(disabled)* | Serve Prometheus metrics on `host:port` (daemon mode only). |
| `-config` | `EY_CONFIG` | *(per-user config dir)* | Path to the YAML config file. |
| `-version` | — | | Print version and exit. |

An invalid `EY_*` value is reported only when the matching flag is not set
explicitly, since an explicit flag takes precedence over the environment.

### Config file

Settings live in `~/.config/ey/config.yaml`, either comma-separated scalars or
YAML lists for `metrics`:

```yaml
disk_threshold: 80
mem_threshold: 90
cpu_threshold: 90
path: /
metrics:           # or "metrics: disk,memory,cpu"
  - disk
  - memory
  - cpu
webhook: https://hooks.slack.com/services/xxx
format: auto
daemon: true
interval: 60s
alert_mode: always
cooldown: 10m
timeout: 5s
json: false
color: auto
log_format: text
log_level: info
quiet: false
retries: 3
retry_backoff: 500ms
state_file: ""
no_state: false

# Top-process metric (used when "process" is in metrics).
process_count: 5
process_sort: cpu
process_threshold: 90
process_count_threshold: 0

prometheus_listen: ""   # e.g. ":9101" to serve metrics in daemon mode
```

The file is merged field-by-field, so a partial file such as
`disk_threshold: 95` is valid and leaves everything else at its default.
Unknown keys are ignored. Flags and `EY_*` variables always win over the file.

### Interactive configuration

`ey config` runs an interactive wizard that walks through every setting,
validates each answer, and writes the result atomically with `0600`
permissions. Values are seeded from your existing config file, and pressing
`Enter` keeps the current value.

```console
# Run the wizard.
ey config

# Print the resolved config path.
ey config -path
# -> /home/you/.config/ey/config.yaml

# Print the effective configuration (defaults merged with the file) as YAML.
ey config -print

# Point at a specific file.
ey config -config /etc/ey/config.yaml
```

The wizard only runs on an interactive terminal; when stdin is not a TTY it
refuses and suggests `ey config -print` instead, so scripts and service
managers can never get stuck on a prompt.

### Alert formats

`-format auto` (the default) detects the payload shape from the webhook host:
`*.slack.com` → Slack, `discord.com` / `discordapp.com` → Discord, everything
else → generic.

- **Slack** — `{"text": ":rotating_light: *System alert* — …"}`
- **Discord** — `{"content": ":rotating_light: *System alert* — …"}`
- **Generic** — the full structured payload:

  ```json
  {
    "snapshot": {
      "timestamp": "2026-10-09T15:02:15+03:00",
      "disk_used_percent": 91.2,
      "disk_total_bytes": 509943480320,
      "disk_used_bytes": 465000000000,
      "mem_used_percent": 68.5,
      "mem_total_bytes": 16428015616,
      "mem_used_bytes": 11264143360,
      "cpu_used_percent": 12.4,
      "process_count": 312,
      "process_top_percent": 42.7,
      "processes": [
        {"pid": 4821, "name": "chrome", "cpu_percent": 42.7, "rss_bytes": 1288490188, "mem_percent": 7.8}
      ]
    },
    "breaches": [
      {"metric": "disk", "value": 91.2, "threshold": 80}
    ]
  }
  ```

### Alert modes

- **`always`** — alert on every check that breaches a threshold.
- **`once`** — alert the first time a threshold is breached, then stay silent
  until all metrics recover; re-arms on recovery.
- **`cooldown`** — alert at most once per `-cooldown`, then suppress further
  alerts for that window.

`once` and `cooldown` state is persisted to disk so it survives daemon restarts
and separate process invocations (e.g. a systemd timer).

### State persistence

By default alert state is stored in a per-user location:

| Platform | Path |
| --- | --- |
| Linux / BSD | `$XDG_STATE_HOME/ey/state.json` or `~/.local/state/ey/state.json` |
| Other | `$(os.UserConfigDir)/ey/state.json` |

The file is written atomically (`0600`) and recovers gracefully from a missing
or corrupt file. Override the location with `-state-file`, or disable
persistence entirely with `-no-state`. The bundled systemd units set
`XDG_STATE_HOME` to the unit's `StateDirectory` (`/var/lib/ey`).

### Webhook retries

Delivery retries transport failures, `429`, and `5xx` responses; other `4xx`
responses are returned immediately and never retried. Backoff is exponential
with full jitter, capped at 8s, and respects a server `Retry-After` header as a
lower bound. Each attempt is bounded by `-timeout`, and cancelling the process
(SIGINT/SIGTERM) aborts in-flight requests and backoff waits. Set `-retries 0`
to disable retries.

### Prometheus metrics

In daemon mode `ey` can expose the metrics it collects on an HTTP endpoint for
Prometheus to scrape. The endpoint is disabled by default; enable it with
`-prometheus-listen host:port` (or `EY_PROMETHEUS_LISTEN`):

```console
ey -daemon -prometheus-listen :9101
```

Because the endpoint must stay up to be scraped, `-prometheus-listen` requires
`-daemon`; setting it without `-daemon` is a hard error. Bare `:9101` binds all
interfaces, so restrict access with a firewall or a reverse proxy, or bind a
single interface such as `127.0.0.1:9101`. Only `GET /metrics` is routed; every
other path returns `404`. Ports below 1024 require the `CAP_NET_BIND_SERVICE`
capability.

Only the metrics selected with `-metrics` are exported (a disabled metric is not
registered at all). All series use the `ey_` prefix:

| Metric | Labels | Meaning |
| --- | --- | --- |
| `ey_build_info` | `version` | Always `1`; carries the build version. |
| `ey_disk_used_percent` | | Disk space used, as a percentage. |
| `ey_disk_total_bytes` | | Total disk space in bytes. |
| `ey_disk_used_bytes` | | Used disk space in bytes. |
| `ey_memory_used_percent` | | Memory used, as a percentage. |
| `ey_memory_total_bytes` | | Total memory in bytes. |
| `ey_memory_used_bytes` | | Used memory in bytes. |
| `ey_cpu_used_percent` | | CPU used, as a percentage. |
| `ey_process_count` | | Number of running processes. |
| `ey_process_top_percent` | | Usage percent of the top process in the sort dimension (`cpu` or `memory`). |
| `ey_process_count_threshold` | | Configured process count threshold (`0` disables the count check). |
| `ey_threshold_percent` | `metric` | Configured threshold for the metric. |
| `ey_threshold_breached` | `metric` | `1` when the metric exceeded its threshold, else `0`. |
| `ey_last_collect_timestamp_seconds` | | Unix time of the last successful collection. |
| `ey_collect_errors_total` | | Failed collections since startup. |

A scrape configuration looks like:

```yaml
scrape_configs:
  - job_name: ey
    static_configs:
      - targets: ["127.0.0.1:9101"]
```

The systemd timer unit (`ey-alert.timer`) runs one-shot checks and therefore
cannot be a scrape target; use the long-running `ey.service` for Prometheus.

## Deployment

### Docker

The image is built from `gcr.io/distroless/static-debian12:nonroot` (it ships CA
certificates for HTTPS webhooks and runs as a non-root user). Build and run
locally with the Makefile:

```console
make docker        # docker build -t ey:<version> .
make docker-run    # runs against the host root filesystem
```

To inspect the host's disk, mount it and point `-path` at the mount point:

```console
docker run --rm --pid=host -v /:/host:ro ghcr.io/emran-ctrl/ey:latest \
  -path /host -disk-threshold 85 -webhook https://hooks.slack.com/...
```

The image's default command is `-json`; pass `-daemon` to run continuously:

```console
docker run -d --name ey --pid=host -v /:/host:ro \
  -e EY_WEBHOOK=https://hooks.slack.com/... \
  -e EY_ALERT_MODE=cooldown \
  ghcr.io/emran-ctrl/ey:latest -path /host -daemon
```

### systemd

Hardened units are provided in [`deploy/`](deploy):

| Unit | Purpose |
| --- | --- |
| `ey.service` | Long-running daemon (`Type=simple`). |
| `ey-alert.service` | One-shot check with `-exit-code` (breach ⇒ failed unit). |
| `ey-alert.timer` | Runs `ey-alert.service` every 5 minutes. |
| `ey.env.example` | Example `EnvironmentFile` with all `EY_*` settings. |

All units source `/etc/ey/ey.env`, use `StateDirectory=ey` (so alert state lands
in `/var/lib/ey`), and apply sandboxing (`ProtectSystem=strict`, `ProtectHome`,
`NoNewPrivileges`, empty capability set, and more).

Install:

```console
sudo install -m 0755 ey /usr/local/bin/ey
sudo install -d /etc/ey
sudo install -m 0640 deploy/ey.env.example /etc/ey/ey.env
sudo install -m 0644 deploy/ey.service deploy/ey-alert.service \
  deploy/ey-alert.timer /etc/systemd/system/
sudo systemctl daemon-reload
```

Then enable the daemon, the timer, or both:

```console
sudo systemctl enable --now ey.service          # continuous daemon
sudo systemctl enable --now ey-alert.timer      # periodic one-shot
```

Edit `/etc/ey/ey.env` to set your webhook and thresholds before starting.

## Output

Human-readable output goes to **stdout**; structured logs go to **stderr**.
`-json` prints a single pretty-printed object in one-shot mode, or compact
NDJSON (one object per line) in daemon mode. In daemon mode the metrics block is
suppressed unless `-json` is set, so normal operation relies on logs. Only
metrics selected with `-metrics` (default all three) are printed, logged, and
evaluated for alerts; text output is ANSI-colorized when connected to a TTY
(`-color auto`), and can be forced with `-color always` or disabled with
`-color never`.

One-shot JSON (with `-metrics cpu,memory`):

```json
{
  "timestamp": "2026-10-09T15:02:15.046869595+03:00",
  "mem_used_percent": 68.56667063932744,
  "mem_total_bytes": 16428015616,
  "mem_used_bytes": 11264143360,
  "cpu_used_percent": 3.7313432849765538
}
```

`breaches` is included only when at least one threshold is exceeded.

### Top processes

The process metric is enabled either by naming `process` in `-metrics`, or
implicitly by setting any `-process-*` option (or its `EY_PROCESS_*` / YAML
equivalent). An explicit metric selection at the same or a higher precedence
level still wins: `-metrics cpu -process-count 8` shows CPU only, whereas a CLI
`-process-count` overrides a `metrics:` list in the config file.

When enabled, `ey` samples per-process CPU over a one-second window and lists
the top `-process-count` processes ranked by `-process-sort` (`cpu` or
`memory`). Text output adds a table:

```text
Processes: 312 running
Top 5 by cpu:
  4821   chrome                    42.7% cpu     7.8% mem    1228.6 MiB
  918    node                      18.2% cpu     2.1% mem     331.0 MiB
```

JSON output adds `process_count`, `process_top_percent`, and a `processes`
array (each entry has `pid`, `name`, `cpu_percent`, `rss_bytes`, and
`mem_percent`). Two breaches are evaluated: `process` when the top consumer's
usage percent exceeds `-process-threshold`, and `process_count` when the total
process count exceeds `-process-count-threshold` (disabled when `0`).

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success (or no breach without `-exit-code`). |
| `1` | A threshold was breached and `-exit-code` was set. |
| `2` | Configuration error, or metrics could not be collected. |

## Building from source

Requires **Go 1.27**. Git is optional (used to derive the version string;
otherwise the version is `dev`).

```console
make build     # -> bin/ey
make dist      # cross-compile: linux/darwin × amd64/arm64 -> bin/ey-<os>-<arch>
make run       # go run ./cmd/ey ARGS="..."
make snapshot  # release artifacts to dist/ (requires goreleaser)
make clean
```

`make` targets:

| Target | Description |
| --- | --- |
| `build` | Build the binary for the host platform. |
| `run` | `go run` the CLI; pass flags via `ARGS="..."`. |
| `test` | `go test ./...` |
| `vet` | `go vet ./...` |
| `fmt` | `gofmt -l -w .` |
| `lint` | `fmt` + `vet` + `test`. |
| `dist` | Cross-compile release binaries. |
| `snapshot` | Build release artifacts locally with goreleaser. |
| `docker` / `docker-run` | Build / run the container image. |
| `clean` | Remove `bin/`. |

## Development

```console
make lint        # gofmt check + go vet + tests
go test -race ./...
```

The version is injected at build time into
`ey/internal/config.BuildVersion` via `-ldflags`:

```console
go build -ldflags "-X ey/internal/config.BuildVersion=$(git describe --tags)" ./cmd/ey
```

CI (`.github/workflows/ci.yml`) runs gofmt, vet, tests, a `-race` job, and a
build + smoke test on every push and pull request.

## Releasing

Releases are automated with [GoReleaser](https://goreleaser.com) and GitHub
Actions (`.github/workflows/release.yml`). Pushing a semver tag builds
linux/darwin archives for amd64/arm64, generates checksums, publishes a GitHub
Release, and pushes a multi-arch image to GHCR:

```console
git tag v0.1.0
git push origin v0.1.0
```

## Project layout

```text
cmd/ey/            CLI entry point, flag wiring, daemon loop, alert policy,
                   config subcommand and text/JSON/color output
internal/config/   Flag + EY_* environment parsing, YAML config file load/
                   write, validation
internal/monitor/  Metric collection (gopsutil), selection, threshold eval
internal/promexporter/ Prometheus registry and metrics HTTP endpoint
internal/alerter/  Webhook payload formatting and retrying HTTP delivery
internal/wizard/   Interactive config wizard (ey config)
internal/state/    Atomic JSON state persistence
internal/fsutil/   Shared atomic file writes
deploy/            systemd units and example EnvironmentFile
```

## Requirements

- **Build:** Go 1.27
- **Runtime:** none — the binary is statically linked and needs no external
  services or interpreters (Linux/macOS, amd64/arm64). Go module dependencies:
  `gopsutil`, `go.yaml.in/yaml/v3`, and `prometheus/client_golang`.
- **Optional:** Docker (container image), systemd (service units),
  GoReleaser (release artifacts)

## License

Released under the [MIT License](LICENSE).
