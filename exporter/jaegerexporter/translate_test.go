package jaegerexporter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/malayh/otel-modules/exporter/jaegerexporter/internal/dbmodel"
)

func TestToDBModelFormat(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "acme")
	rs.Resource().Attributes().PutStr("host.name", "h1")

	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("scope")
	ss.Scope().SetVersion("1.0")

	start := time.Unix(1600000000, 123456000)
	sp := ss.Spans().AppendEmpty()
	sp.SetTraceID(pcommon.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10})
	sp.SetSpanID(pcommon.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18})
	sp.SetParentSpanID(pcommon.SpanID{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28})
	sp.SetName("op")
	sp.SetKind(ptrace.SpanKindServer)
	sp.Status().SetCode(ptrace.StatusCodeError)
	sp.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	sp.SetEndTimestamp(pcommon.NewTimestampFromTime(start.Add(5 * time.Millisecond)))
	sp.Attributes().PutInt("http.status_code", 500)
	ev := sp.Events().AppendEmpty()
	ev.SetName("ev")
	ev.SetTimestamp(pcommon.NewTimestampFromTime(start))

	spans := ToDBModel(traces)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	elevateTags(&spans[0])
	s := spans[0]

	if s.StartTime != 1600000000123456 {
		t.Errorf("StartTime = %d", s.StartTime)
	}
	if s.StartTimeMillis != 1600000000123 {
		t.Errorf("StartTimeMillis = %d", s.StartTimeMillis)
	}
	if s.Duration != 5000 {
		t.Errorf("Duration = %d", s.Duration)
	}
	if string(s.TraceID) != "0102030405060708090a0b0c0d0e0f10" {
		t.Errorf("TraceID = %s", s.TraceID)
	}
	if s.Process.ServiceName != "acme" {
		t.Errorf("ServiceName = %s", s.Process.ServiceName)
	}
	if len(s.References) != 1 || s.References[0].RefType != dbmodel.ChildOf || string(s.References[0].SpanID) != "2122232425262728" {
		t.Errorf("References = %+v", s.References)
	}

	if s.Tag["span@kind"] != "server" {
		t.Errorf("elevated span@kind = %v", s.Tag["span@kind"])
	}
	if s.Tag["error"] != true {
		t.Errorf("elevated error = %v", s.Tag["error"])
	}
	for _, kv := range s.Tags {
		if kv.Key == "span.kind" || kv.Key == "error" {
			t.Errorf("tag %q should have been elevated out of tags", kv.Key)
		}
	}
	if !hasTag(s.Tags, "http.status_code", dbmodel.Int64Type) {
		t.Errorf("missing http.status_code int64 tag: %+v", s.Tags)
	}
	if !hasTag(s.Tags, "otel.scope.name", dbmodel.StringType) {
		t.Errorf("missing otel.scope.name tag: %+v", s.Tags)
	}
	if !hasTag(s.Process.Tags, "host.name", dbmodel.StringType) {
		t.Errorf("missing host.name process tag: %+v", s.Process.Tags)
	}
	if len(s.Logs) != 1 || !hasTag(s.Logs[0].Fields, "event", dbmodel.StringType) {
		t.Errorf("logs = %+v", s.Logs)
	}

	raw, err := json.Marshal(&s)
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	if strings.Contains(js, "@timestamp") {
		t.Errorf("@timestamp must not be written for legacy indices: %s", js)
	}
	for _, want := range []string{`"startTimeMillis"`, `"tag":{`, `"span@kind":"server"`, `"error":true`, `"refType":"CHILD_OF"`} {
		if !strings.Contains(js, want) {
			t.Errorf("marshaled span missing %s\n%s", want, js)
		}
	}
}

func TestIndexNaming(t *testing.T) {
	cases := []struct{ prefix, base, want string }{
		{"", "jaeger-span", "jaeger-span"},
		{"traces-acme", "jaeger-span", "traces-acme-jaeger-span"},
		{"traces-acme-", "jaeger-span", "traces-acme-jaeger-span"},
	}
	for _, c := range cases {
		if got := joinPrefix(c.prefix, c.base); got != c.want {
			t.Errorf("joinPrefix(%q,%q) = %q want %q", c.prefix, c.base, got, c.want)
		}
	}
}

func hasTag(kvs []dbmodel.KeyValue, key string, typ dbmodel.ValueType) bool {
	for _, kv := range kvs {
		if kv.Key == key && kv.Type == typ {
			return true
		}
	}
	return false
}
