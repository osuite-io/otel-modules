# jaegerexporter

Writes OTLP traces to OpenSearch in Jaeger v2's ES/OpenSearch index format, so existing Jaeger UI/query readers keep working. Write-only, traces-only. Replaces the per-tenant `jaeger_storage` + `jaeger_storage_exporter` setup.

See `spec.md` for design and fidelity notes.

## Config

```yaml
exporters:
  jaegerexporter:
    endpoints: ["https://opensearch:9200"]
    username: ${ES_USERNAME}
    password: ${ES_PASSWORD}
    tls:
      ca_file: /tls/ca.crt
      insecure_skip_verify: true
    index_prefix: "traces-acme"
    check_index_template: true
```

| field | default | notes |
|---|---|---|
| `endpoints` | required | OpenSearch URLs |
| `username` / `password` | "" | basic auth; env-expanded |
| `tls` | — | OTel `configtls.ClientConfig` |
| `index_prefix` | "" | `<prefix>-jaeger-span-<UTC date>` |
| `check_index_template` | `true` | crash on start if no template covers the write index |
| `date_layout` | `2006-01-02` | daily; `2006-01-02-15` = hourly |
| `service_cache_ttl` | `12h` | service:operation dedup TTL |
| `bulk` | 5MB / 1000 / 200ms / 1 | `max_bytes` / `max_actions` / `flush_interval` / `workers` |

## Behavior

- Index date derives from span start time (UTC).
- Span docs: bulk `index`, auto `_id`, typeless. Service docs: `_id = fnv1a64hex(service+op)`, LRU-deduped.
- `span.kind` + `error` elevated into the `tag` map (dots → `@`).
- Best-effort: bulk errors are logged and dropped, never retried; the collector stays up. Pending writes flush on shutdown.
- Does not create indices, templates, or mappings.
