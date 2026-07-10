package jaegerexporter

import (
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/collector/config/configopaque"
	"go.opentelemetry.io/collector/config/configtls"
)

type Config struct {
	Endpoints          []string               `mapstructure:"endpoints"`
	Username           string                 `mapstructure:"username"`
	Password           configopaque.String    `mapstructure:"password"`
	TLS                configtls.ClientConfig `mapstructure:"tls"`
	IndexPrefix        string                 `mapstructure:"index_prefix"`
	CheckIndexTemplate bool                   `mapstructure:"check_index_template"`
	DateLayout         string                 `mapstructure:"date_layout"`
	ServiceCacheTTL    time.Duration          `mapstructure:"service_cache_ttl"`
	Bulk               BulkConfig             `mapstructure:"bulk"`
}

type BulkConfig struct {
	MaxBytes      int           `mapstructure:"max_bytes"`
	MaxActions    int           `mapstructure:"max_actions"`
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	Workers       int           `mapstructure:"workers"`
}

func (c *Config) Validate() error {
	if len(c.Endpoints) == 0 {
		return errors.New("jaegerexporter: at least one endpoint is required")
	}
	if strings.ContainsAny(c.IndexPrefix, " \t\n\r") {
		return errors.New("jaegerexporter: index_prefix must not contain whitespace")
	}
	return nil
}
