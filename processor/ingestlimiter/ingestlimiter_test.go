package ingestlimiter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor/processortest"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestLimiter(t *testing.T, cfg *Config, signal string) *limiter {
	t.Helper()
	l, err := newLimiter(cfg, processortest.NewNopSettings(componentType), signal)
	if err != nil {
		t.Fatalf("newLimiter: %v", err)
	}
	return l
}

func TestShouldReject(t *testing.T) {
	fresh := time.Now()
	stale := time.Now().Add(-time.Hour)
	tests := []struct {
		name     string
		decision decision
		failures int
		lastGood time.Time
		failOpen bool
		want     bool
	}{
		{"never fail-open accepts", decisionNever, 0, time.Time{}, true, false},
		{"never fail-closed rejects", decisionNever, 0, time.Time{}, false, true},
		{"nolimit accepts", decisionNoLimit, 0, fresh, false, false},
		{"under fresh accepts", decisionUnderBudget, 0, fresh, true, false},
		{"under stale fail-open accepts", decisionUnderBudget, 0, stale, true, false},
		{"under stale fail-closed rejects", decisionUnderBudget, 0, stale, false, true},
		{"over fresh fail-open rejects", decisionOverBudget, 0, fresh, true, true},
		{"over fresh fail-closed rejects", decisionOverBudget, 0, fresh, false, true},
		{"over valve fail-open accepts", decisionOverBudget, failOpenValve, fresh, true, false},
		{"over valve fail-closed rejects", decisionOverBudget, failOpenValve, fresh, false, true},
		{"over stale fail-open accepts", decisionOverBudget, 0, stale, true, false},
		{"over stale fail-closed rejects", decisionOverBudget, 0, stale, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &limiter{
				cfg:          &Config{FailOpen: tt.failOpen, MaxStaleness: time.Minute},
				decision:     tt.decision,
				failures:     tt.failures,
				lastGoodTime: tt.lastGood,
			}
			if got := l.shouldReject(); got != tt.want {
				t.Errorf("shouldReject()=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestPollDecision(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		wantErr      bool
		wantDecision decision
	}{
		{"over budget", 200, `{"traces":{"used":10,"allowed":5}}`, false, decisionOverBudget},
		{"under budget", 200, `{"traces":{"used":1,"allowed":5}}`, false, decisionUnderBudget},
		{"signal absent", 200, `{"logs":{"used":1,"allowed":5}}`, false, decisionNoLimit},
		{"allowed zero", 200, `{"traces":{"used":1,"allowed":0}}`, false, decisionNoLimit},
		{"non-2xx", 500, `boom`, true, decisionNever},
		{"malformed body", 200, `not json`, true, decisionNever},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			l := newTestLimiter(t, &Config{QuotaEndpoint: srv.URL, RequestTimeout: time.Second}, "traces")
			err := l.poll(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("poll err=%v wantErr=%v", err, tt.wantErr)
			}
			if l.decision != tt.wantDecision {
				t.Errorf("decision=%v want=%v", l.decision, tt.wantDecision)
			}
		})
	}
}

func TestPollSignalKeyOverride(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"custom":{"used":10,"allowed":5}}`)
	}))
	defer srv.Close()
	l := newTestLimiter(t, &Config{
		QuotaEndpoint:  srv.URL,
		RequestTimeout: time.Second,
		SignalKey:      "custom",
		AuthHeader:     "Authorization",
		AuthValue:      "Bearer tok",
	}, "traces")
	if err := l.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if l.decision != decisionOverBudget {
		t.Errorf("decision=%v want overBudget", l.decision)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header=%q want %q", gotAuth, "Bearer tok")
	}
}

func TestPollFailureIncrements(t *testing.T) {
	l := newTestLimiter(t, &Config{QuotaEndpoint: "http://127.0.0.1:0", RequestTimeout: 200 * time.Millisecond}, "traces")
	err := l.poll(context.Background())
	if err == nil {
		t.Fatal("expected poll error")
	}
	l.onPollFailure(err)
	if l.failures != 1 {
		t.Errorf("failures=%d want 1", l.failures)
	}
}

func TestConsumeTracesReject(t *testing.T) {
	l := newTestLimiter(t, &Config{FailOpen: false, MaxStaleness: time.Minute, RetryAfter: 42 * time.Second}, "traces")
	l.nextTraces = consumertest.NewNop()
	l.decision = decisionOverBudget
	l.lastGoodTime = time.Now()

	err := l.ConsumeTraces(context.Background(), ptrace.NewTraces())
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		t.Fatalf("got %v, want ResourceExhausted", err)
	}
	found := false
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			if ri.RetryDelay.AsDuration() != 42*time.Second {
				t.Errorf("retry delay=%v want 42s", ri.RetryDelay.AsDuration())
			}
			found = true
		}
	}
	if !found {
		t.Error("RetryInfo detail missing")
	}
}

func TestConsumeTracesForward(t *testing.T) {
	sink := new(consumertest.TracesSink)
	l := newTestLimiter(t, &Config{FailOpen: true, MaxStaleness: time.Minute}, "traces")
	l.nextTraces = sink
	l.decision = decisionUnderBudget
	l.lastGoodTime = time.Now()

	if err := l.ConsumeTraces(context.Background(), ptrace.NewTraces()); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := len(sink.AllTraces()); got != 1 {
		t.Errorf("forwarded batches=%d want 1", got)
	}
}

func TestFailOpenValveAfterFailures(t *testing.T) {
	l := newTestLimiter(t, &Config{FailOpen: true, MaxStaleness: time.Hour}, "traces")
	l.decision = decisionOverBudget
	l.lastGoodTime = time.Now()
	if !l.shouldReject() {
		t.Fatal("expected reject before failures")
	}
	for i := 0; i < failOpenValve; i++ {
		l.onPollFailure(errors.New("boom"))
	}
	if l.shouldReject() {
		t.Fatal("expected accept after 3 failures (fail-open valve)")
	}
}

func TestFailClosedIgnoresValve(t *testing.T) {
	l := newTestLimiter(t, &Config{FailOpen: false, MaxStaleness: time.Hour}, "traces")
	l.decision = decisionOverBudget
	l.lastGoodTime = time.Now()
	for i := 0; i < failOpenValve+2; i++ {
		l.onPollFailure(errors.New("boom"))
	}
	if !l.shouldReject() {
		t.Fatal("fail-closed must keep rejecting despite failures")
	}
}
