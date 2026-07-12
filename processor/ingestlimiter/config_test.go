package ingestlimiter

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	base := Config{
		QuotaEndpoint:  "http://quota",
		PollInterval:   time.Minute,
		RequestTimeout: time.Second,
		MaxStaleness:   time.Minute,
	}
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr bool
	}{
		{"happy", func(*Config) {}, false},
		{"missing endpoint", func(c *Config) { c.QuotaEndpoint = "" }, true},
		{"bad poll_interval", func(c *Config) { c.PollInterval = 0 }, true},
		{"bad request_timeout", func(c *Config) { c.RequestTimeout = 0 }, true},
		{"bad max_staleness", func(c *Config) { c.MaxStaleness = -1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
