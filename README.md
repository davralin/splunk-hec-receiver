# Splunk HEC Receiver

`splunk-hec-receiver` is a lightweight, Kubernetes-friendly HTTP Event Collector
(HEC) target for validating the [Splunk OpenTelemetry Collector
chart](https://github.com/signalfx/splunk-otel-collector-chart) without a Splunk
instance. It captures recent HEC envelopes for inspection; it is not a Splunk
replacement or persistent telemetry backend.

## Supported HEC Surface

- `POST /services/collector/event`
- `POST /services/collector`
- `GET /services/collector/health`
- JSON and gzip-compressed newline-delimited HEC envelopes

Valid requests return `{"text":"Success","code":0}`. The receiver accepts
any token by default. Set `HEC_ACCEPTED_TOKENS` to a comma-separated allowlist to
require `Authorization: Splunk <token>`.

## Inspection

- `GET /healthz` returns HTTP 200 when the process is serving requests.
- `GET /api/v1/events?after=<id>&limit=<1-1000>` returns captured HEC envelopes.
- `GET /api/v1/status` returns capture capacity and acceptance/eviction counters.

Authorization headers and token values are never retained or returned.

## Collector Example

```yaml
splunkPlatform:
  endpoint: http://splunk-hec-receiver:8088/services/collector/event
  token: test-token
  index: main
```

The chart enables logs by default. Set `metricsEnabled: true` with `metricsIndex`,
or `tracesEnabled: true` with `tracesIndex`, to validate those exporter pipelines.

## Resource Safety

Captured telemetry is retained only in a bounded FIFO buffer. Oldest events are
evicted first. Request body size, decompressed gzip size, individual retained event
size, capture bytes/event count, and concurrent request processing all have
independent limits. See [ADR 0009](adr/0009-use-bounded-in-memory-hec-capture.md).

| Variable | Default | Description |
|---|---:|---|
| `HEC_LISTEN_ADDRESS` | `:8088` | HTTP listener address |
| `HEC_ACCEPTED_TOKENS` | unset | Optional comma-separated accepted HEC tokens |
| `HEC_MAX_REQUEST_BYTES` | `4MiB` | Maximum compressed request body |
| `HEC_MAX_DECOMPRESSED_BYTES` | `16MiB` | Maximum decoded request body |
| `HEC_MAX_CAPTURE_BYTES` | `64MiB` | Total retained envelope bytes |
| `HEC_MAX_EVENT_BYTES` | `1MiB` | Maximum individually retained envelope |
| `HEC_MAX_EVENTS` | `10000` | Maximum retained envelope count |
| `HEC_MAX_CONCURRENT_REQUESTS` | `4` | Maximum concurrent ingestion requests |

Deploy with enough memory for the capture budget and request-processing headroom;
`256MiB` is a reasonable starting limit for the defaults.

## Development

```sh
go test ./...
docker build -f Containerfile -t splunk-hec-receiver:local .
docker run --rm -p 8088:8088 splunk-hec-receiver:local
curl -H 'Authorization: Splunk test-token' \
  -H 'Content-Type: application/json' \
  --data '{"event":"hello"}' \
  http://localhost:8088/services/collector/event
curl http://localhost:8088/api/v1/events
```

The single release image is built for `linux/amd64` and `linux/arm64`, published to
GHCR on the inherited weekly CalVer release workflow, and carries BuildKit SBOM and
SLSA provenance. Deploy immutable image digests where possible.
