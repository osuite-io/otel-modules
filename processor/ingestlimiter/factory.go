package ingestlimiter

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
)

var componentType = component.MustNewType("ingestlimiter")

const stability = component.StabilityLevelDevelopment

func NewFactory() processor.Factory {
	return processor.NewFactory(
		componentType,
		createDefaultConfig,
		processor.WithTraces(createTraces, stability),
		processor.WithLogs(createLogs, stability),
		processor.WithMetrics(createMetrics, stability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		AuthHeader:     "Authorization",
		PollInterval:   5 * time.Minute,
		RequestTimeout: 10 * time.Second,
		FailOpen:       true,
		MaxStaleness:   15 * time.Minute,
		RetryAfter:     5 * time.Minute,
	}
}

func createTraces(_ context.Context, set processor.Settings, cfg component.Config, next consumer.Traces) (processor.Traces, error) {
	l, err := newLimiter(cfg.(*Config), set, "traces")
	if err != nil {
		return nil, err
	}
	l.nextTraces = next
	return l, nil
}

func createLogs(_ context.Context, set processor.Settings, cfg component.Config, next consumer.Logs) (processor.Logs, error) {
	l, err := newLimiter(cfg.(*Config), set, "logs")
	if err != nil {
		return nil, err
	}
	l.nextLogs = next
	return l, nil
}

func createMetrics(_ context.Context, set processor.Settings, cfg component.Config, next consumer.Metrics) (processor.Metrics, error) {
	l, err := newLimiter(cfg.(*Config), set, "metrics")
	if err != nil {
		return nil, err
	}
	l.nextMetrics = next
	return l, nil
}
