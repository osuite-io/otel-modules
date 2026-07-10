FROM golang:1.25 AS build
WORKDIR /build
COPY manifest.yaml .
RUN CGO_ENABLED=0 go run go.opentelemetry.io/collector/cmd/builder@v0.156.0 --config manifest.yaml

FROM gcr.io/distroless/static:nonroot
COPY --from=build /build/_build/otelcol-contrib /otelcol-contrib
ENTRYPOINT ["/otelcol-contrib"]
