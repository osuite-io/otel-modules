package jaegerexporter

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
)

var componentType = component.MustNewType("jaegerexporter")

const stability = component.StabilityLevelDevelopment

func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		componentType,
		createDefaultConfig,
		exporter.WithTraces(createTraces, stability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		CheckIndexTemplate: true,
		DateLayout:         "2006-01-02",
		ServiceCacheTTL:    12 * time.Hour,
		Bulk: BulkConfig{
			MaxBytes:      5000000,
			MaxActions:    1000,
			FlushInterval: 200 * time.Millisecond,
			Workers:       1,
		},
	}
}

func createTraces(_ context.Context, set exporter.Settings, cfg component.Config) (exporter.Traces, error) {
	return newExporter(cfg.(*Config), set), nil
}
