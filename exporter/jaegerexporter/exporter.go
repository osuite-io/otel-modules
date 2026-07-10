package jaegerexporter

import (
	"context"
	"hash/fnv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/olivere/elastic/v7"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/malayh/otel-modules/exporter/jaegerexporter/internal/dbmodel"
)

type jaegerExporter struct {
	cfg    *Config
	logger *zap.Logger

	client *elastic.Client
	bulk   *elastic.BulkProcessor

	serviceCache *expirable.LRU[string, struct{}]

	spanIndexBase    string
	serviceIndexBase string
}

func newExporter(cfg *Config, set exporter.Settings) *jaegerExporter {
	return &jaegerExporter{
		cfg:              cfg,
		logger:           set.Logger,
		serviceCache:     expirable.NewLRU[string, struct{}](100000, nil, cfg.ServiceCacheTTL),
		spanIndexBase:    joinPrefix(cfg.IndexPrefix, "jaeger-span"),
		serviceIndexBase: joinPrefix(cfg.IndexPrefix, "jaeger-service"),
	}
}

func (*jaegerExporter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (e *jaegerExporter) Start(ctx context.Context, _ component.Host) error {
	client, err := newElasticClient(ctx, e.cfg)
	if err != nil {
		return err
	}
	e.client = client

	if e.cfg.CheckIndexTemplate {
		if err := e.checkIndexTemplates(ctx); err != nil {
			return err
		}
	}

	bulk, err := client.BulkProcessor().
		Workers(e.cfg.Bulk.Workers).
		BulkActions(e.cfg.Bulk.MaxActions).
		BulkSize(e.cfg.Bulk.MaxBytes).
		FlushInterval(e.cfg.Bulk.FlushInterval).
		After(e.afterBulk).
		Do(context.Background())
	if err != nil {
		return err
	}
	e.bulk = bulk
	return nil
}

func (e *jaegerExporter) ConsumeTraces(_ context.Context, td ptrace.Traces) error {
	spans := ToDBModel(td)
	for i := range spans {
		span := &spans[i]
		elevateTags(span)

		date := time.UnixMicro(int64(span.StartTime)).UTC().Format(e.cfg.DateLayout)

		svc := dbmodel.Service{
			ServiceName:   span.Process.ServiceName,
			OperationName: span.OperationName,
		}
		key := hashCode(svc)
		if !e.serviceCache.Contains(key) {
			e.bulk.Add(elastic.NewBulkIndexRequest().
				Index(e.serviceIndexBase + "-" + date).
				Id(key).
				Doc(svc))
			e.serviceCache.Add(key, struct{}{})
		}

		e.bulk.Add(elastic.NewBulkIndexRequest().
			Index(e.spanIndexBase + "-" + date).
			Doc(span))
	}
	return nil
}

func (e *jaegerExporter) Shutdown(context.Context) error {
	if e.bulk != nil {
		if err := e.bulk.Close(); err != nil {
			e.logger.Error("jaegerexporter: bulk processor close failed", zap.Error(err))
		}
	}
	if e.client != nil {
		e.client.Stop()
	}
	return nil
}

func (e *jaegerExporter) afterBulk(_ int64, _ []elastic.BulkableRequest, response *elastic.BulkResponse, err error) {
	if err != nil {
		e.logger.Error("jaegerexporter: bulk request failed", zap.Error(err))
	}
	if response != nil && response.Errors {
		for _, item := range response.Items {
			for _, v := range item {
				if v.Error != nil {
					e.logger.Error("jaegerexporter: bulk item failed",
						zap.String("index", v.Index),
						zap.Int("status", v.Status),
						zap.Any("error", v.Error))
				}
			}
		}
	}
}

func newElasticClient(ctx context.Context, cfg *Config) (*elastic.Client, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	tlsCfg, err := cfg.TLS.LoadTLSConfig(ctx)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsCfg

	opts := []elastic.ClientOptionFunc{
		elastic.SetURL(cfg.Endpoints...),
		elastic.SetSniff(false),
		elastic.SetHealthcheck(false),
		elastic.SetHttpClient(&http.Client{Transport: transport}),
	}
	if cfg.Username != "" {
		opts = append(opts, elastic.SetBasicAuth(cfg.Username, string(cfg.Password)))
	}
	return elastic.NewClient(opts...)
}

func joinPrefix(prefix, name string) string {
	if prefix == "" {
		return name
	}
	if strings.HasSuffix(prefix, "-") {
		return prefix + name
	}
	return prefix + "-" + name
}

func hashCode(s dbmodel.Service) string {
	h := fnv.New64a()
	h.Write([]byte(s.ServiceName))
	h.Write([]byte(s.OperationName))
	return strconv.FormatUint(h.Sum64(), 16)
}
