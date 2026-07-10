otelcol_version := "0.156.0"
builder := "CGO_ENABLED=0 go run go.opentelemetry.io/collector/cmd/builder@v" + otelcol_version
image := "otelcol-custom:" + otelcol_version
compose := "docker compose -f sanity/docker-compose.yaml"

default:
    @just --list

build:
    {{builder}} --config manifest.yaml

validate: build
    ./_build/otelcol-contrib validate --config sanity/otel-validate.yaml

image:
    docker build -t {{image}} .

sanity: image
    {{compose}} up -d otelcol
    timeout 90 sh -c 'until curl -sf localhost:13133 >/dev/null; do sleep 1; done'
    {{compose}} run --rm telemetrygen
    {{compose}} logs otelcol
    {{compose}} down -v

down:
    {{compose}} down -v

clean:
    rm -rf _build
