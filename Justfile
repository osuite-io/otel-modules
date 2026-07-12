otelcol_version := "0.156.0"
builder := "CGO_ENABLED=0 go run go.opentelemetry.io/collector/cmd/builder@v" + otelcol_version
image := "otelcol-custom:" + otelcol_version
registry := "ghcr.io/osuite-io/otelcol-custom"
compose := "docker compose -f sanity/docker-compose.yaml"
compose_il := "docker compose -f sanity/docker-compose.ingestlimiter.yaml"

default:
    @just --list

build:
    {{builder}} --config manifest.yaml

validate: build
    ./_build/otelcol-contrib validate --config sanity/otel-validate.yaml

image:
    docker build -t {{image}} .

gcr-login:
    echo "${GHCR_TOKEN:?set GHCR_TOKEN to a GitHub PAT with write:packages scope}" | docker login ghcr.io -u "${GHCR_USER:?set GHCR_USER to your GitHub username}" --password-stdin

release version:
    docker tag {{image}} {{registry}}:{{version}}
    docker tag {{image}} {{registry}}:latest
    docker push {{registry}}:{{version}}
    docker push {{registry}}:latest

sanity: image
    {{compose}} up -d otelcol
    timeout 90 sh -c 'until curl -sf localhost:13133 >/dev/null; do sleep 1; done'
    {{compose}} run --rm telemetrygen
    {{compose}} logs otelcol
    {{compose}} down -v

sanity-ingestlimiter: image
    #!/usr/bin/env bash
    set -euo pipefail
    trap "{{compose_il}} down -v" EXIT
    {{compose_il}} up -d otelcol quota-stub
    timeout 90 sh -c 'until curl -sf localhost:13133 >/dev/null; do sleep 1; done'
    sleep 4
    out=$({{compose_il}} run --rm telemetrygen 2>&1 || true)
    echo "$out"
    echo "$out" | grep -qi "ResourceExhausted"
    echo "PASS: collector returned ResourceExhausted (429) on quota breach"

down:
    {{compose}} down -v

clean:
    rm -rf _build
