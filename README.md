Custom modules for otel collector. It uses the  otel contrib collector 0.156.0

## Exporters
- `jaegerexporter`: Exports spans in jaeger format to opensearch. The writen spans are jaeger 2.1.0 compatibe. This is to replace the jaeger collector. It doesn't handle index lifecycle or exprity, those needs to be externally handled. 

## Processors
- `ingestlimiter`: Processor that enforces quota on total logs,metrics,spans ingested. It uses an external API get the used and allowed limits and returns 429 if used>allowed.