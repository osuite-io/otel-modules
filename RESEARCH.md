# Custom OpenTelemetry Collector Distribution — Research

Goal: host extra collector modules (receivers, processors, exporters, connectors/extensions) in this repo, then compile a custom collector binary (contrib components + our modules) and ship it as a container image.

## Key concept

- Collector components are **compiled into one Go binary** — you cannot bolt Go components onto a prebuilt `otelcol-contrib` image.
- "On top of contrib" = an OCB manifest that lists the contrib components we want **plus** our local modules, then compiles a fresh binary.
- The official [`opentelemetry-collector-releases`](https://github.com/open-telemetry/opentelemetry-collector-releases) repo is the canonical example of this pattern.

## Tools required

- **Go** — compiles the distribution (match version to the collector release; check the collector repo's `go.mod`).
- **OCB (`ocb`)** — OpenTelemetry Collector Builder; reads a manifest, generates Go source, resolves deps, compiles the binary. Current: `v0.156.0`.
- **`mdatagen`** — generates boilerplate (config docs, telemetry code) from each component's `metadata.yaml`. Run via `go generate`.
- **Docker** — multi-stage build to package the binary into a minimal image.
- **Make** (optional) — wrap `generate` / `build` / `docker` targets.

Install OCB:
```bash
curl --proto '=https' --tlsv1.2 -fL -o ocb \
  https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fbuilder%2Fv0.156.0/ocb_0.156.0_linux_amd64
chmod +x ocb
```

## Proposed repo structure

- One Go module **per component** (keeps deps minimal for custom builds); a top-level manifest ties them together.

```
otel-modules/
├── receiver/myreceiver/      # go.mod, factory.go, config.go, metadata.yaml, doc.go, README.md
├── processor/myprocessor/
├── exporter/myexporter/
├── connector/myconnector/
├── extension/myextension/
├── builder-config.yaml       # OCB manifest (contrib + local modules)
├── Dockerfile                # multi-stage: build binary -> minimal image
├── Makefile
└── RESEARCH.md
```

## Custom component anatomy

Each component module contains:

- `metadata.yaml` — declares `type`, `status.class` (receiver/processor/exporter/connector/extension), stability, supported signals, codeowners.
- `doc.go` — holds the `//go:generate mdatagen metadata.yaml` pragma.
- `factory.go` — `NewFactory()` returning a `<class>.Factory`; wires default config + create functions.
- `config.go` — the `Config` struct + `Validate()`.
- `<component>.go` — the actual logic (implements `component.Component`: `Start`/`Shutdown`, plus the class-specific interface).
- `README.md`, `go.mod`.
- Generate boilerplate with `go generate ./...`.

## OCB manifest (`builder-config.yaml`)

- `dist` — output metadata (binary name, version, module path, `output_path`).
- Component lists — `receivers`, `processors`, `exporters`, `extensions`, `connectors`, `providers`.
- Local (unpublished) modules use `gomod` **plus** `path` to point at the folder.
- `replaces` — replace directives for local/forked modules if not using `path`.

```yaml
dist:
  module: github.com/malayh/otel-modules
  name: otelcol-custom
  description: Custom OpenTelemetry Collector distribution
  output_path: ./_build
  version: 0.1.0

receivers:
  - gomod: go.opentelemetry.io/collector/receiver/otlpreceiver v0.156.0
  - gomod: github.com/malayh/otel-modules/receiver/myreceiver v0.1.0
    path: ./receiver/myreceiver          # local, not yet published

processors:
  - gomod: go.opentelemetry.io/collector/processor/batchprocessor v0.156.0
  - gomod: go.opentelemetry.io/collector/processor/memorylimiterprocessor v0.156.0   # always include
  - gomod: github.com/malayh/otel-modules/processor/myprocessor v0.1.0
    path: ./processor/myprocessor

exporters:
  - gomod: go.opentelemetry.io/collector/exporter/debugexporter v0.156.0
  - gomod: github.com/malayh/otel-modules/exporter/myexporter v0.1.0
    path: ./exporter/myexporter

extensions:
  - gomod: github.com/malayh/otel-modules/extension/myextension v0.1.0
    path: ./extension/myextension

connectors:
  - gomod: github.com/malayh/otel-modules/connector/myconnector v0.1.0
    path: ./connector/myconnector

providers:
  - gomod: go.opentelemetry.io/collector/confmap/provider/envprovider v1.48.0
  - gomod: go.opentelemetry.io/collector/confmap/provider/fileprovider v1.48.0
```

## Build process

1. `go generate ./...` — regenerate component boilerplate from `metadata.yaml`.
2. `./ocb --config builder-config.yaml` — generates source + compiles binary into `output_path` (`./_build`).
3. `docker build .` — multi-stage image around the compiled binary.

## Docker packaging (multi-stage)

```dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN go run go.opentelemetry.io/collector/cmd/builder@v0.156.0 --config builder-config.yaml

FROM gcr.io/distroless/static:nonroot
COPY --from=build /src/_build/otelcol-custom /otelcol-custom
COPY config.yaml /etc/otelcol/config.yaml
ENTRYPOINT ["/otelcol-custom"]
CMD ["--config", "/etc/otelcol/config.yaml"]
```

- Runtime config (`config.yaml`, the pipeline definition) is separate from the build manifest — baked in or mounted at runtime.
- Base image: distroless/static or `alpine` for a small footprint; official images use distroless.

## Gotchas / notes

- **Dual versioning**: stable collector modules are `v1.x` (e.g. confmap providers `v1.48.0`); unstable ones are `v0.156.0`. Keep the `v0.x` set aligned to the same collector release.
- **Version alignment**: `ocb` version, all component versions, and the runtime Go version should track one collector release to avoid dependency conflicts.
- **Start minimal**: include only components the runtime pipeline uses; always add `memorylimiterprocessor`.
- **Publishing modules is optional**: components can live only in this repo (referenced by `path`) — no need to publish to the registry to build/run them.

## Not covered (per scope)

- CI/CD (GitHub Actions), GoReleaser multi-arch releases, image signing (cosign), SBOM — deferred.

## References

- [OCB docs](https://opentelemetry.io/docs/collector/extend/ocb/)
- [Building your own distribution (J. Kröhling)](https://medium.com/opentelemetry/building-your-own-opentelemetry-collector-distribution-42337e994b63)
- [contrib: new-components.md](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/docs/new-components.md)
- [opentelemetry-collector-releases](https://github.com/open-telemetry/opentelemetry-collector-releases)
- [Collector distributions](https://opentelemetry.io/docs/collector/distributions/)
