package ingestlimiter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type decision int

const (
	decisionNever decision = iota
	decisionNoLimit
	decisionUnderBudget
	decisionOverBudget
)

const failOpenValve = 3

type quotaEntry struct {
	Used    float64 `json:"used"`
	Allowed float64 `json:"allowed"`
}

type limiter struct {
	cfg    *Config
	logger *zap.Logger
	signal string
	key    string
	client *http.Client

	nextTraces  consumer.Traces
	nextLogs    consumer.Logs
	nextMetrics consumer.Metrics

	mu           sync.Mutex
	decision     decision
	used         float64
	allowed      float64
	lastGoodTime time.Time
	failures     int

	cancel        context.CancelFunc
	wg            sync.WaitGroup
	signalAttr    attribute.Set
	rejectedItems metric.Int64Counter
	pollFailures  metric.Int64Counter
	reg           metric.Registration
}

func newLimiter(cfg *Config, set processor.Settings, signal string) (*limiter, error) {
	key := signal
	if cfg.SignalKey != "" {
		key = cfg.SignalKey
	}
	l := &limiter{
		cfg:        cfg,
		logger:     set.Logger,
		signal:     signal,
		key:        key,
		client:     &http.Client{Timeout: cfg.RequestTimeout},
		signalAttr: attribute.NewSet(attribute.String("signal", signal)),
	}
	if err := l.initMetrics(set); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *limiter) initMetrics(set processor.Settings) error {
	meter := set.MeterProvider.Meter("github.com/malayh/otel-modules/processor/ingestlimiter")
	var err error
	if l.rejectedItems, err = meter.Int64Counter("ingestlimiter.rejected_items"); err != nil {
		return err
	}
	if l.pollFailures, err = meter.Int64Counter("ingestlimiter.poll_failures"); err != nil {
		return err
	}
	usageRatio, err := meter.Float64ObservableGauge("ingestlimiter.usage_ratio")
	if err != nil {
		return err
	}
	lastPollAge, err := meter.Float64ObservableGauge("ingestlimiter.last_poll_age")
	if err != nil {
		return err
	}
	l.reg, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		l.mu.Lock()
		ratio := 0.0
		if l.allowed > 0 {
			ratio = l.used / l.allowed
		}
		age := 0.0
		if !l.lastGoodTime.IsZero() {
			age = time.Since(l.lastGoodTime).Seconds()
		}
		l.mu.Unlock()
		o.ObserveFloat64(usageRatio, ratio, metric.WithAttributeSet(l.signalAttr))
		o.ObserveFloat64(lastPollAge, age, metric.WithAttributeSet(l.signalAttr))
		return nil
	}, usageRatio, lastPollAge)
	return err
}

func (l *limiter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (l *limiter) Start(_ context.Context, _ component.Host) error {
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.wg.Add(1)
	go l.pollLoop(ctx)
	return nil
}

func (l *limiter) Shutdown(_ context.Context) error {
	if l.cancel != nil {
		l.cancel()
	}
	l.wg.Wait()
	if l.reg != nil {
		_ = l.reg.Unregister()
	}
	return nil
}

func (l *limiter) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if l.shouldReject() {
		return l.reject(ctx)
	}
	return l.nextTraces.ConsumeTraces(ctx, td)
}

func (l *limiter) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	if l.shouldReject() {
		return l.reject(ctx)
	}
	return l.nextLogs.ConsumeLogs(ctx, ld)
}

func (l *limiter) ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	if l.shouldReject() {
		return l.reject(ctx)
	}
	return l.nextMetrics.ConsumeMetrics(ctx, md)
}

func (l *limiter) shouldReject() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch l.decision {
	case decisionNoLimit:
		return false
	case decisionUnderBudget:
		if l.stale() {
			return !l.cfg.FailOpen
		}
		return false
	case decisionOverBudget:
		if l.cfg.FailOpen && l.failures >= failOpenValve {
			return false
		}
		if l.stale() {
			return !l.cfg.FailOpen
		}
		return true
	default:
		return !l.cfg.FailOpen
	}
}

func (l *limiter) stale() bool {
	if l.lastGoodTime.IsZero() {
		return false
	}
	return time.Since(l.lastGoodTime) > l.cfg.MaxStaleness
}

func (l *limiter) reject(ctx context.Context) error {
	l.rejectedItems.Add(ctx, 1, metric.WithAttributes(
		attribute.String("signal", l.signal),
		attribute.String("reason", "quota"),
	))
	st := status.New(codes.ResourceExhausted, "ingestlimiter: storage quota exceeded for "+l.signal)
	if withInfo, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(l.cfg.RetryAfter)}); err == nil {
		return withInfo.Err()
	}
	return st.Err()
}

func (l *limiter) pollLoop(ctx context.Context) {
	defer l.wg.Done()
	backoff := l.cfg.PollInterval
	if backoff > time.Second {
		backoff = time.Second
	}
	for {
		wait := l.cfg.PollInterval
		if err := l.poll(ctx); err != nil {
			l.onPollFailure(err)
			wait = backoff
			backoff *= 2
			if backoff > l.cfg.PollInterval {
				backoff = l.cfg.PollInterval
			}
		} else {
			backoff = l.cfg.PollInterval
			if backoff > time.Second {
				backoff = time.Second
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (l *limiter) poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.cfg.QuotaEndpoint, nil)
	if err != nil {
		return err
	}
	if l.cfg.AuthValue != "" {
		req.Header.Set(l.cfg.AuthHeader, string(l.cfg.AuthValue))
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ingestlimiter: quota endpoint returned status %d", resp.StatusCode)
	}
	var payload map[string]quotaEntry
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	l.logger.Info("ingestlimiter: quota poll succeeded",
		zap.String("signal", l.signal),
		zap.ByteString("response", body))
	entry, ok := payload[l.key]
	l.applyPoll(entry, ok)
	return nil
}

func (l *limiter) applyPoll(entry quotaEntry, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures = 0
	l.lastGoodTime = time.Now()
	l.used = entry.Used
	l.allowed = entry.Allowed
	switch {
	case !ok || entry.Allowed <= 0:
		l.decision = decisionNoLimit
	case entry.Used >= entry.Allowed:
		l.decision = decisionOverBudget
	default:
		l.decision = decisionUnderBudget
	}
}

func (l *limiter) onPollFailure(err error) {
	l.mu.Lock()
	l.failures++
	failures := l.failures
	l.mu.Unlock()
	l.pollFailures.Add(context.Background(), 1, metric.WithAttributeSet(l.signalAttr))
	l.logger.Warn("ingestlimiter: quota poll failed",
		zap.String("signal", l.signal),
		zap.Int("consecutive_failures", failures),
		zap.Error(err))
}
