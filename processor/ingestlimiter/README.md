# ingestlimiter

Rejects incoming batches per signal (traces / logs / metrics) when a **storage quota** is exceeded. Polls an external HTTP endpoint for `{used, allowed}` per signal; when `used >= allowed` it returns gRPC `ResourceExhausted` + `RetryInfo`, which the OTLP receiver turns into a wire-level **429 / Retry-After**.

Place it **first, before `batch`** — a rejection must propagate back to the receiver synchronously (anything after `batch` is too late; the receiver already ACKed). Reference the single processor block in all three pipelines; each instance learns its signal from the pipeline it sits in.

Single-scope. Multi-tenancy is handled upstream. See `../../design/ratelimit.md` for design.

## Config

```yaml
processors:
  ingestlimiter:
    quota_endpoint: http://quota-svc/quota
    auth_header: Authorization
    auth_value: "Bearer ${QUOTA_KEY}"
    poll_interval: 5m
    request_timeout: 10s
    fail_open: true
    max_staleness: 15m
    retry_after: 5m

service:
  pipelines:
    traces:  { receivers: [otlp], processors: [ingestlimiter, batch], exporters: [...] }
    logs:    { receivers: [otlp], processors: [ingestlimiter, batch], exporters: [...] }
    metrics: { receivers: [otlp], processors: [ingestlimiter, batch], exporters: [...] }
```

| field | default | notes |
|---|---|---|
| `quota_endpoint` | required | `GET` returns `{ "<signal>": { "used", "allowed" } }` per signal |
| `auth_header` | `Authorization` | request header carrying `auth_value` |
| `auth_value` | "" | header value; env-expanded, opaque |
| `poll_interval` | `5m` | how often the background poller refreshes usage |
| `request_timeout` | `10s` | per-request HTTP timeout |
| `fail_open` | `true` | master switch: on outage/staleness/uncertainty, `true` accepts, `false` rejects |
| `max_staleness` | `15m` | last good poll older than this ⇒ governed by `fail_open` |
| `retry_after` | `5m` | value sent in the reject's `RetryInfo` / `Retry-After` |
| `signal_key` | "" | override the response key (default `traces`/`logs`/`metrics`); one value applies to all instances |

## Quota endpoint

```
GET <quota_endpoint>   <auth_header>: <auth_value>

200 OK
{ "traces":  { "used": 4200000000, "allowed": 5000000000 },
  "logs":    { "used": 1000000000, "allowed": 5000000000 },
  "metrics": { "used": 3000000,    "allowed": 5000000    } }
```

`used`/`allowed` are compared as opaque numbers — any extra fields (e.g. `unit`) are ignored.

## Behavior

- Decision is `O(1)` per batch off the last cached poll; the poller runs in the background.
- **Fail-open cases** (accept when `fail_open: true`, reject when `false`): no successful poll yet, or last good poll older than `max_staleness`.
- **No limit** (always accept): signal absent from the response, or `allowed <= 0`.
- Poll failures (unreachable / non-2xx / unparseable) retry with exponential backoff (capped at `poll_interval`) and keep the last cached decision.
- **Fast valve:** if over budget and the endpoint fails 3 consecutive times, accept — but only when `fail_open: true`. Fail-closed keeps rejecting through outages.
- No durable state: on restart it polls immediately; until the first success, `fail_open` governs.
- Over-budget batches are rejected whole; the batch is never mutated.

## Metrics

- `ingestlimiter.usage_ratio` — gauge, per signal (`used/allowed`)
- `ingestlimiter.rejected_items` — counter, attr `reason=quota`
- `ingestlimiter.poll_failures` — counter
- `ingestlimiter.last_poll_age` — gauge (seconds since last good poll)
