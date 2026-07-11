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
	rs.Resource().Attributes().PutInt("process.pid", 1)

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
	sp.Status().SetMessage("boom")
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

	if s.ParentSpanID != "" {
		t.Errorf("ParentSpanID must not be populated (jaeger 2.1.0 uses references only), got %s", s.ParentSpanID)
	}
	if s.Flags != 0 {
		t.Errorf("Flags must not be populated, got %d", s.Flags)
	}
	if s.Tag != nil {
		t.Errorf("Tag map must be nil (no tag elevation), got %v", s.Tag)
	}

	if !hasTagValue(s.Tags, "span.kind", dbmodel.StringType, "server") {
		t.Errorf("span.kind must stay in tags as string 'server': %+v", s.Tags)
	}
	if !hasTagValue(s.Tags, "otel.status_code", dbmodel.StringType, "ERROR") {
		t.Errorf("missing otel.status_code=ERROR: %+v", s.Tags)
	}
	if !hasTagValue(s.Tags, "error", dbmodel.BoolType, "true") {
		t.Errorf("missing error=true (bool, stringified): %+v", s.Tags)
	}
	if !hasTagValue(s.Tags, "otel.status_description", dbmodel.StringType, "boom") {
		t.Errorf("missing otel.status_description: %+v", s.Tags)
	}
	if !hasTagValue(s.Tags, "http.status_code", dbmodel.Int64Type, "500") {
		t.Errorf("http.status_code must be stringified int64 '500': %+v", s.Tags)
	}
	if !hasTagValue(s.Tags, "otel.scope.name", dbmodel.StringType, "scope") {
		t.Errorf("missing otel.scope.name tag: %+v", s.Tags)
	}
	if !hasTagValue(s.Process.Tags, "process.pid", dbmodel.Int64Type, "1") {
		t.Errorf("process.pid must be stringified int64 '1': %+v", s.Process.Tags)
	}
	if !hasTagValue(s.Process.Tags, "host.name", dbmodel.StringType, "h1") {
		t.Errorf("missing host.name process tag: %+v", s.Process.Tags)
	}
	if len(s.Logs) != 1 || !hasTagValue(s.Logs[0].Fields, "event", dbmodel.StringType, "ev") {
		t.Errorf("logs = %+v", s.Logs)
	}

	raw, err := json.Marshal(&s)
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, bad := range []string{"@timestamp", `"tag":{`, "span@kind", "parentSpanID", `"flags"`} {
		if strings.Contains(js, bad) {
			t.Errorf("marshaled span must not contain %q\n%s", bad, js)
		}
	}
	for _, want := range []string{
		`"startTimeMillis"`,
		`"refType":"CHILD_OF"`,
		`"key":"span.kind","type":"string","value":"server"`,
		`"key":"otel.status_code","type":"string","value":"ERROR"`,
		`"key":"error","type":"bool","value":"true"`,
		`"key":"http.status_code","type":"int64","value":"500"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("marshaled span missing %s\n%s", want, js)
		}
	}
}

func TestToDBModelEmptySlices(t *testing.T) {
	traces := ptrace.NewTraces()
	rs := traces.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "acme")
	ss := rs.ScopeSpans().AppendEmpty()

	start := time.Unix(1600000000, 0)
	sp := ss.Spans().AppendEmpty()
	sp.SetName("root")
	sp.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	sp.SetEndTimestamp(pcommon.NewTimestampFromTime(start))

	spans := ToDBModel(traces)
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]

	if s.References == nil || len(s.References) != 0 {
		t.Errorf("References must be empty non-nil slice, got %#v", s.References)
	}
	if s.Logs == nil || len(s.Logs) != 0 {
		t.Errorf("Logs must be empty non-nil slice, got %#v", s.Logs)
	}
	if s.Tags == nil {
		t.Errorf("Tags must be non-nil, got nil")
	}

	raw, err := json.Marshal(&s)
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, want := range []string{`"references":[]`, `"logs":[]`, `"tags":[]`} {
		if !strings.Contains(js, want) {
			t.Errorf("root span must serialize %s (not null)\n%s", want, js)
		}
	}
	for _, bad := range []string{"parentSpanID", `"flags"`, `"tag":{`, "@timestamp", "null"} {
		if strings.Contains(js, bad) {
			t.Errorf("root span must not contain %q\n%s", bad, js)
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

func hasTagValue(kvs []dbmodel.KeyValue, key string, typ dbmodel.ValueType, value string) bool {
	for _, kv := range kvs {
		if kv.Key == key && kv.Type == typ && kv.Value == value {
			return true
		}
	}
	return false
}
