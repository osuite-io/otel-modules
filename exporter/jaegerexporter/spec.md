# jaegerexporter — spec

Custom OTel Collector exporter that writes OTLP traces to OpenSearch in **Jaeger v2's exact ES/OpenSearch index format**, replacing the per-tenant Jaeger collector (`jaeger_storage` extension + `jaeger_storage_exporter`) in `osuite_infra/charts/observability-tenant/templates/jaeger-collector.yaml`.

Config key: `jaeger_opensearch`. Go package: `exporter/jaegerexporter`.

## Goal

- Consume OTLP traces, write span + service docs to OpenSearch, byte-compatible with what Jaeger writes today, so the existing Jaeger UI/query keeps working unchanged.
- Optional startup check: crash if no index template covers the write index (we never create templates).

## Scope

In:
- Traces only. Write-only. OpenSearch (ES7-compatible API) only.
- Periodic **daily UTC** date-rollover indices (current prod layout).
- Span index + service index (with dedup), matching Jaeger defaults.
- Async bulk write, best-effort (log + drop on error), flush on shutdown — mimics current Jaeger behavior.
- Optional index-template existence check (pattern match, default ON).

Out (deferred / not needed):
- Reads, dependency storage, sampling storage, metrics storage.
- Index/template/ILM creation, aliases, data streams, rollover.
- `tags_as_fields` config surface (defaults are hardcoded — see Data model).
- Tenant routing (done upstream by routing connector).
- Jaeger's internal `sanitizer` pre-pass.
- ES v8/v9 composable-template write path (`_type`, `_index_template` PUT) — not our target.

## Behavior parity (what we replicate from Jaeger v2)

Verified against `github.com/jaegertracing/jaeger` `internal/storage/...`:

- Index names from **span start time** (not ingest time), UTC.
- Span doc: bulk `index` op, **auto `_id`**, **no `_type`** (OpenSearch is typeless).
- Service doc: bulk `index` op, `_id = fnv1a64hex(serviceName + operationName)`, deduped via LRU (100k, 12h TTL).
- Default elevation of `span.kind` + `error` into the `tag` map (dots→`@`).
- Async bulk: 5MB / 1000 actions / 200ms / 1 worker; flush on close.
- Errors logged + counted, **never retried or propagated**; `ConsumeTraces` returns nil.

## Config

```yaml
exporters:
  jaeger_opensearch:
    endpoints: ["https://<release>-opensearch.<ns>.svc.cluster.local:9200"]
    username: ${ES_USERNAME}
    password: ${ES_PASSWORD}
    tls:                        # OTel configtls.ClientConfig
      insecure: false
      ca_file: /tls/ca.crt
      insecure_skip_verify: true
    index_prefix: "traces-<tenant>"
    check_index_template: true  # default true; crash on Start if no template matches
    # optional, defaults mirror Jaeger:
    # date_layout: "2006-01-02"
    # service_cache_ttl: 12h
    # bulk: { max_bytes: 5000000, max_actions: 1000, flush_interval: 200ms, workers: 1 }
```

Fields:

| field | type | default | notes |
|---|---|---|---|
| `endpoints` | []string | required | OpenSearch URLs |
| `username` / `password` | string / configopaque | "" | inline, env-expanded; internally sets Basic `Authorization` header. No auth extension. |
| `tls` | configtls.ClientConfig | — | `insecure:false` loads `ca_file`, then `insecure_skip_verify` skips server verify (Jaeger's behavior) |
| `index_prefix` | string | "" | joined as `<prefix>-jaeger-span`; `-` auto-added; trailing `-` not doubled |
| `check_index_template` | bool | **true** | see Template check |
| `date_layout` | string | `2006-01-02` | daily; `2006-01-02-15` = hourly |
| `service_cache_ttl` | duration | 12h | service dedup LRU TTL |
| `bulk` | struct | 5MB/1000/200ms/1 | olivere BulkProcessor knobs |

`Validate()`: `endpoints` non-empty; `index_prefix` no whitespace.

Dropped vs Jaeger config (only fed template creation, which we don't do): `shards`, `replicas`, `create_mappings`, `version`.

## Data model (document format)

Copied verbatim from Jaeger (see Code reuse). Span JSON = `dbmodel.Span`:

- `traceID` (32-char lowercase hex), `spanID`/`parentSpanID` (16-char hex; parent omitted for root).
- `operationName`, `flags` (uint32, omitempty).
- `references`: `[{refType: CHILD_OF|FOLLOWS_FROM, traceID, spanID}]`. Parent emitted as first `CHILD_OF` (back-compat). Links → refs (`opentracing.ref_type` attr → type, default FOLLOWS_FROM).
- `startTime` (**µs** since epoch), `startTimeMillis` (= startTime/1000, ms; the time-range field), `duration` (**µs**).
- `tags`: `[{key,type,value}]`, type ∈ string|bool|int64|float64|binary. Bytes→hex string(binary); map/slice→JSON string(string).
- `logs`: `[{timestamp(µs), fields:[KeyValue]}]` from span events; event name → `event` field unless already present.
- `process`: `{serviceName, tags:[KeyValue]}`. Empty resource → `serviceName="OTLPResourceNoServiceName"`. `service.name` attr → serviceName; other resource attrs → process.tags.
- Derived span tags (in order): `otel.scope.name`/`otel.scope.version`, span attrs, `span.kind` (client/server/…), status: OK→`otel.status_code=OK`, ERROR→`error=true`(bool); `otel.status_description`; `w3c.tracestate`.
- **Elevated tags** (default): keys `span.kind`, `error` moved from `tags`/`process.tags` into `tag`/`process.tag` maps, key dots→`@` (`span.kind`→`span@kind`). Binary tags never elevated. Maps omitted when empty. → e.g. `"tag": {"span@kind":"server","error":true}`.
- `@timestamp`: **not written** (data-stream only).

Service JSON = `{"serviceName","operationName"}`.

## Index naming

- Span: `<prefix>-jaeger-span-<UTC date>` (e.g. `traces-acme-jaeger-span-2026-07-10`).
- Service: `<prefix>-jaeger-service-<UTC date>`.
- Constants: base `jaeger-span`/`jaeger-service`, separator `-`, date = `startTime.UTC().Format(date_layout)`.

## Write path

- olivere/elastic v7 client + `BulkProcessor` (mimics Jaeger). Built in `Start()`.
- `ConsumeTraces`: `ToDBModel(td)` → per span: enqueue service doc (if not in dedup cache) then span doc via `bulkProc.Add()`. Return nil.
- Dedup: LRU(100k, `service_cache_ttl`) keyed by `fnv1a64hex(service+op)`; same value is the service doc `_id`.
- `After` callback logs per-item + per-batch errors, records metrics. Not propagated.
- `Shutdown`: `bulkProc.Close()` (flush) then client stop.
- exporterhelper: timeout=0, retry disabled, queue disabled (best-effort parity).

## Template existence check

- `check_index_template: true` (default). Runs in `Start()`; returns error ⇒ collector crashes.
- Fetch templates via **both** legacy `GET /_template` and composable `GET /_index_template` (operator may have used either on OpenSearch).
- Compute representative write-index names (span + service, today UTC). For each, require ≥1 template whose `index_patterns` matches (ES `*` glob). If either has no match → error listing the missing pattern (e.g. `*<prefix>-jaeger-span-*`).
- Rationale: writes go to date-suffixed indices that don't exist until first write, and a missing template makes OpenSearch auto-create with dynamic mappings (silent corruption of the format) — so we check the template, not the index.

## Code reuse (Jaeger conversion is `internal/` → must copy)

Copy into `exporter/jaegerexporter/internal/dbmodel/`:
- `dbmodel/model.go` (structs: Span, Reference, Process, Log, KeyValue, Service + type aliases/consts).
- `to_dbmodel.go` → rename pkg; swap internal `otelsemconv` import for a local `~7 const` file (`service.name`, `otel.status_code`, `otel.status_description`, `otel.scope.name`, `otel.scope.version`, `opentracing.ref_type`(+`child_of`)).
- Port `splitElevatedTags` (from `core/writer.go`) for the elevation step; hardcode `tagKeysAsFields={span.kind,error}`, `dotReplacement="@"`, `allTagsAsFields=false`.

Import (public, allowed): `github.com/jaegertracing/jaeger-idl/model/v1` (for `TimeAsEpochMicroseconds`, `DurationAsMicroseconds`, `SpanKind*`, `SpanKindKey`).

Do **not** copy: `ids.go` (read side), reader, dot_replacer (read side), olivere wrapper (use olivere directly).

## Component layout

```
exporter/jaegerexporter/
  metadata.yaml        # type: jaeger_opensearch, class exporter, traces, dev stability
  doc.go               # //go:generate mdatagen metadata.yaml
  factory.go           # NewFactory, default config, createTraces via exporterhelper
  config.go            # Config + Validate
  exporter.go          # Start (client, bulk, template check) / ConsumeTraces / Shutdown
  translate.go         # ToDBModel + elevation (copied/ported)
  template_check.go    # pattern-match check
  internal/dbmodel/    # copied structs + semconv consts
  go.mod
  README.md
```

Manifest (`manifest.yaml`) add:
```yaml
exporters:
  - gomod: github.com/malayh/otel-modules/exporter/jaegerexporter v0.1.0
    path: ./exporter/jaegerexporter
```

## Success criteria

1. `just build` compiles the distro with the exporter.
2. Sanity (local OpenSearch + template preloaded): `telemetrygen traces` → docs land in `<prefix>-jaeger-span-<date>` / `-service-<date>`; a written span doc diffs equal (modulo `_id`) to one produced by `jaegertracing/jaeger:2.1.0` with the same config against the same cluster.
3. Jaeger UI (pointed at the same OpenSearch) lists the service/operation and renders the trace.
4. Template missing + `check_index_template:true` → collector exits nonzero at start with a clear message; `false` → starts.
5. OpenSearch unreachable mid-run → errors logged, collector stays up, no panic (parity with today).

## Open items

- Confirm exporter `type` name `jaegerexporter`, not `jaeger_opensearch` : Confired
- Helm: rewrite `jaeger-collector.yaml` to run `otelcol-custom` with pipeline `otlp → batch → jaeger_opensearch` (follow-up; not this spec). Ignore
- Partial-bulk-failure durability (item-level retry) is a possible future upgrade beyond current parity: Ignore for now
