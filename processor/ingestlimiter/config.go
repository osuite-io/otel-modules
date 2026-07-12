package ingestlimiter

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/config/configopaque"
)

type Config struct {
	QuotaEndpoint  string              `mapstructure:"quota_endpoint"`
	AuthHeader     string              `mapstructure:"auth_header"`
	AuthValue      configopaque.String `mapstructure:"auth_value"`
	PollInterval   time.Duration       `mapstructure:"poll_interval"`
	RequestTimeout time.Duration       `mapstructure:"request_timeout"`
	FailOpen       bool                `mapstructure:"fail_open"`
	MaxStaleness   time.Duration       `mapstructure:"max_staleness"`
	RetryAfter     time.Duration       `mapstructure:"retry_after"`
	SignalKey      string              `mapstructure:"signal_key"`
}

func (c *Config) Validate() error {
	if c.QuotaEndpoint == "" {
		return errors.New("ingestlimiter: quota_endpoint is required")
	}
	if c.PollInterval <= 0 {
		return errors.New("ingestlimiter: poll_interval must be > 0")
	}
	if c.RequestTimeout <= 0 {
		return errors.New("ingestlimiter: request_timeout must be > 0")
	}
	if c.MaxStaleness <= 0 {
		return errors.New("ingestlimiter: max_staleness must be > 0")
	}
	return nil
}
