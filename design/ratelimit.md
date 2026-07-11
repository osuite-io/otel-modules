# Ingest Limiting — Design

Date: 2026-07-11 · Status: design approved, rate-cap sourcing left open

## Problem

Protect the collector's downstream store and pipeline from over-ingestion, in two independent ways, per signal (traces / logs / metrics):

- **Storage quota** — cap total data *resident in the backend store* (e.g. spans ≤ 5 GB, logs ≤ 5 GB, metrics ≤ 5 GB or ≤ 5 M samples). A backstop: retention normally keeps usage under the cap; the limiter only fires on runaway growth.
- **Rate cap** — cap ingestion throughput (spans/sec, log-records/sec, data-points/sec).

Tripping either gate → reject the batch → receiver returns **429 / gRPC ResourceExhausted + Retry-After**.

Single-scope. Multi-tenancy is handled upstream by the routing connector; this component knows nothing about tenants.

## Key decisions

| Decision | Choice | Why |
|---|---|---|
| Component type for enforcement | **Processor** (not connector/extension) | To push a 429 back to the client, must reject *synchronously in the receiver's consume path, before `batch`*. A connector emits into a new pipeline and can't do this; the receiver-boundary extension interface is still experimental on 0.156.0. |
| What "storage quota" measures | **Stock** (data resident in store), not flow (bytes ingested over time) | Retention deletes data out-of-band and store-size ≠ ingested-bytes (compression, replicas, index overhead). Only the backend knows the true number. |
| Source of the usage number | **External HTTP endpoint** returning `{used, allowed}` per signal | Decouples the collector from every backend. Endpoint owner computes usage however they like (OpenSearch `_cat/indices`, TSDB stats, …). Collector compares two opaque numbers. |
| Quota limit location | **From the endpoint** (`allowed`) | Quota policy is global; lives in one external place. Change the cap without touching the collector. |
| Rate limit location | **Collector config** | Rate limiting protects local throughput — a per-instance concern, not global policy. |
| Accuracy target | **±10%**, poll every ~5 min, use last polled value directly | At target scale (~1 GB/day ≈ 12 KB/s) a 5-min overshoot is a few MB against a 5 GB cap. No per-batch byte counting, no local accumulation, no durable state. |
| On endpoint failure | **Fail-open** (configurable) | A broken quota service must not black-hole all telemetry. Backstop semantics. |
| Poller placement | **Single processor**, one config block, referenced in all 3 pipelines | Simplest; self-contained. 3 pollers hit the same endpoint per interval — negligible. |
| Rate-cap implementation | **OPEN** — reuse Elastic `ratelimitprocessor` (leaning) vs build a small `x/time/rate` token bucket | Reuse = zero code but an external module pinned to a 0.156.0-compatible version + its deps. Build = ~a few dozen lines, no coupling, fits repo. Quota-gate design is identical either way. |

## The two gates

Both evaluated per batch, per signal, independent:

- **Storage quota** — poll external endpoint → cache `{used, allowed}` → gate `used >= allowed`. This is what we build.
- **Rate cap** — in-memory token bucket, records/sec + burst, per signal. Either Elastic's processor or a small token bucket in our module (open decision).

## Architecture & pipeline flow

```
otlp receiver ─▶ [ingestlimiter] ─▶ [ratelimit?] ─▶ batch ─▶ jaegerexporter
                     │  used >= allowed?  → 429 back to receiver
                     └─ background poller  ⇄  GET <quota_endpoint>   (every poll_interval)
```

- Limiter(s) sit **first, before `batch`** — a rejection propagates back to the OTLP receiver synchronously and becomes a wire-level 429. Anything after `batch` is too late (receiver already ACKed).
- `ingestlimiter` is defined once and referenced in the traces / logs / metrics pipelines. Each instance learns its signal from `WithTraces/WithLogs/WithMetrics` and enforces only that signal's budget.
- Per instance: a background goroutine refreshes cached `{used, allowed}` every `poll_interval`; `ConsumeX` does an O(1) `used >= allowed` check, then either rejects or forwards the batch untouched.
- **Reject mechanism:** return `status.Error(codes.ResourceExhausted, …)` with `RetryInfo`. The OTLP receiver maps that to HTTP 429 / gRPC ResourceExhausted + `Retry-After` — the same path `memory_limiter` and Elastic's processor use.

Example pipeline (reuse-Elastic variant):

```yaml
processors:
  ingestlimiter:
    quota_endpoint: http://quota-svc/quota
    auth_value: "Bearer ${QUOTA_KEY}"
    poll_interval: 5m
    fail_open: true
  ratelimit:                 # Elastic — only in the reuse variant
    strategy: records
    rate: 5000
    burst: 10000
    throttle_behavior: error
  batch: {}

service:
  pipelines:
    traces:  { receivers: [otlp], processors: [ingestlimiter, ratelimit, batch], exporters: [jaegerexporter] }
    logs:    { receivers: [otlp], processors: [ingestlimiter, ratelimit, batch], exporters: [...] }
    metrics: { receivers: [otlp], processors: [ingestlimiter, ratelimit, batch], exporters: [...] }
```

In the build-our-own variant, `ratelimit` disappears and `ingestlimiter` gains a `rate:` section.

## Quota endpoint contract

```
GET <quota_endpoint>          Authorization: Bearer <key>

200 OK
{ "traces":  { "used": 4200000000, "allowed": 5000000000, "unit": "bytes"   },
  "logs":    { "used": 1000000000, "allowed": 5000000000, "unit": "bytes"   },
  "metrics": { "used": 3000000,    "allowed": 5000000,    "unit": "samples" } }
```

- Collector compares `used` vs `allowed` as opaque numbers. `unit` is informational (human/metrics only) — the collector never interprets it, so "5 GB vs 5 M samples" is entirely the endpoint's choice.
- A signal absent from the response ⇒ that instance has no limit (fail-open).

## Processor config (`ingestlimiter`)

```yaml
quota_endpoint:  http://quota-svc/quota   # required
auth_header:     Authorization            # optional
auth_value:      "Bearer ${QUOTA_KEY}"    # optional
poll_interval:   5m
request_timeout: 10s
fail_open:       true      # endpoint down / stale / signal missing → accept
max_staleness:   15m       # last-good poll older than this → fail-open kicks in
retry_after:     5m        # value sent in Retry-After header on quota reject
signal_key:      ""        # optional override of the "traces"/"logs"/"metrics" response key
```

`Validate`: `quota_endpoint` required; `poll_interval`, `request_timeout`, `max_staleness` > 0.

## Behavior & edge cases

- **Fail-open** (configurable) on: endpoint unreachable, non-2xx, unparseable body, signal absent, `allowed <= 0`/missing, or last-good poll older than `max_staleness`. Always log + emit a metric.
- **No durable state.** Restart → poll immediately; until the first successful poll, `fail_open` governs.
- Over-budget batches are **rejected whole** — no partial drops.
- Overshoot ≈ ingestion_rate × poll_interval; shorten `poll_interval` if ingestion is bursty.

## Observability (`metadata.yaml` → mdatagen)

- `ingestlimiter.usage_ratio` — gauge, per signal = used/allowed
- `ingestlimiter.rejected_items` — counter, label `reason=quota`
- `ingestlimiter.poll_failures` — counter
- `ingestlimiter.last_poll_age` — gauge (seconds since last good poll)

## Module layout

Follows `RESEARCH.md` component anatomy:

```
processor/ingestlimiter/
  go.mod  metadata.yaml  doc.go (//go:generate mdatagen)
  factory.go  config.go (+Validate)  ingestlimiter.go  README.md
```

Add to `manifest.yaml` under `processors:` with a `path:` entry. In the reuse variant, also add Elastic's `ratelimitprocessor` (pinned to a 0.156.0-compatible version).

## Testing / success criteria

- **Unit:** decision logic (used/allowed → accept/reject), response parsing, every fail-open path, `Validate`.
- **Sanity:** extend `sanity/docker-compose.yaml` with a stub quota endpoint returning `used >= allowed`; run `telemetrygen`; assert the collector returns **429**. Plus `just validate` on the config.

## Open decisions

- **Rate-cap implementation** — reuse Elastic `ratelimitprocessor` (leaning) vs build a small token bucket into `ingestlimiter`. Does not affect the quota-gate design.
