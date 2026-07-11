// Copyright (c) 2025 The Jaeger Authors.
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0
//
// Adapted from github.com/jaegertracing/jaeger
// internal/storage/v2/elasticsearch/tracestore/to_dbmodel.go and
// internal/storage/v2/elasticsearch/tracestore/core/writer.go.

package jaegerexporter

import (
	"encoding/hex"
	"strconv"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/jaegertracing/jaeger-idl/model/v1"

	"github.com/malayh/otel-modules/exporter/jaegerexporter/internal/dbmodel"
)

const (
	serviceNameKey            = "service.name"
	otelStatusCode            = "otel.status_code"
	otelStatusDescription     = "otel.status_description"
	otelScopeName             = "otel.scope.name"
	otelScopeVersion          = "otel.scope.version"
	opentracingRefType        = "opentracing.ref_type"
	opentracingRefTypeChildOf = "child_of"

	noServiceName    = "OTLPResourceNoServiceName"
	eventNameAttr    = "event"
	statusOk         = "OK"
	statusError      = "ERROR"
	tagW3CTraceState = "w3c.tracestate"
)

func ToDBModel(td ptrace.Traces) []dbmodel.Span {
	resourceSpans := td.ResourceSpans()

	if resourceSpans.Len() == 0 {
		return nil
	}

	batches := make([]dbmodel.Span, 0, resourceSpans.Len())
	for i := 0; i < resourceSpans.Len(); i++ {
		rs := resourceSpans.At(i)
		batch := resourceSpansToDbSpans(rs)
		if batch != nil {
			batches = append(batches, batch...)
		}
	}

	return batches
}

func resourceSpansToDbSpans(resourceSpans ptrace.ResourceSpans) []dbmodel.Span {
	resource := resourceSpans.Resource()
	scopeSpans := resourceSpans.ScopeSpans()

	if scopeSpans.Len() == 0 {
		return []dbmodel.Span{}
	}

	process := resourceToDbProcess(resource)

	dbSpans := make([]dbmodel.Span, 0, scopeSpans.At(0).Spans().Len())

	for _, scopeSpan := range scopeSpans.All() {
		for _, span := range scopeSpan.Spans().All() {
			dbSpan := spanToDbSpan(span, scopeSpan.Scope(), process)
			dbSpans = append(dbSpans, dbSpan)
		}
	}

	return dbSpans
}

func resourceToDbProcess(resource pcommon.Resource) dbmodel.Process {
	process := dbmodel.Process{}
	attrs := resource.Attributes()
	tags := make([]dbmodel.KeyValue, 0, attrs.Len())
	for key, attr := range attrs.All() {
		if key == serviceNameKey {
			process.ServiceName = attr.AsString()
			continue
		}
		tags = append(tags, attributeToDbTag(key, attr))
	}
	if process.ServiceName == "" {
		process.ServiceName = noServiceName
	}
	process.Tags = tags
	return process
}

func appendTagsFromAttributes(dest []dbmodel.KeyValue, attrs pcommon.Map) []dbmodel.KeyValue {
	for key, attr := range attrs.All() {
		dest = append(dest, attributeToDbTag(key, attr))
	}
	return dest
}

func attributeToDbTag(key string, attr pcommon.Value) dbmodel.KeyValue {
	switch attr.Type() {
	case pcommon.ValueTypeInt:
		return dbmodel.KeyValue{Key: key, Type: dbmodel.Int64Type, Value: strconv.FormatInt(attr.Int(), 10)}
	case pcommon.ValueTypeBool:
		return dbmodel.KeyValue{Key: key, Type: dbmodel.BoolType, Value: strconv.FormatBool(attr.Bool())}
	case pcommon.ValueTypeDouble:
		return dbmodel.KeyValue{Key: key, Type: dbmodel.Float64Type, Value: strconv.FormatFloat(attr.Double(), 'g', 10, 64)}
	case pcommon.ValueTypeBytes:
		return dbmodel.KeyValue{Key: key, Type: dbmodel.BinaryType, Value: hex.EncodeToString(attr.Bytes().AsRaw())}
	default:
		return dbmodel.KeyValue{Key: key, Type: dbmodel.StringType, Value: attr.AsString()}
	}
}

func spanToDbSpan(span ptrace.Span, libraryTags pcommon.InstrumentationScope, process dbmodel.Process) dbmodel.Span {
	traceID := dbmodel.TraceID(span.TraceID().String())
	parentSpanID := dbmodel.SpanID(span.ParentSpanID().String())
	startTime := span.StartTimestamp().AsTime()
	return dbmodel.Span{
		TraceID:         traceID,
		SpanID:          dbmodel.SpanID(span.SpanID().String()),
		OperationName:   span.Name(),
		References:      linksToDbSpanRefs(span.Links(), parentSpanID, traceID),
		StartTime:       model.TimeAsEpochMicroseconds(startTime),
		StartTimeMillis: model.TimeAsEpochMicroseconds(startTime) / 1000,
		Duration:        model.DurationAsMicroseconds(span.EndTimestamp().AsTime().Sub(startTime)),
		Tags:            getDbSpanTags(span, libraryTags),
		Logs:            spanEventsToDbSpanLogs(span.Events()),
		Process:         process,
	}
}

func getDbSpanTags(span ptrace.Span, scope pcommon.InstrumentationScope) []dbmodel.KeyValue {
	libraryTags, libraryTagsFound := getTagsFromInstrumentationLibrary(scope)
	spanKindTag, spanKindTagFound := getTagFromSpanKind(span.Kind())
	status := span.Status()
	statusTags := getTagsFromStatusCode(status.Code())
	statusMsgTag, statusMsgTagFound := getTagFromStatusMsg(status.Message())
	traceStateTags, traceStateTagsFound := getTagsFromTraceState(span.TraceState().AsRaw())

	tagsCount := span.Attributes().Len() + len(libraryTags) + len(statusTags) + len(traceStateTags)
	if spanKindTagFound {
		tagsCount++
	}
	if statusMsgTagFound {
		tagsCount++
	}

	tags := make([]dbmodel.KeyValue, 0, tagsCount)
	if libraryTagsFound {
		tags = append(tags, libraryTags...)
	}
	tags = appendTagsFromAttributes(tags, span.Attributes())
	if spanKindTagFound {
		tags = append(tags, spanKindTag)
	}
	tags = append(tags, statusTags...)
	if statusMsgTagFound {
		tags = append(tags, statusMsgTag)
	}
	if traceStateTagsFound {
		tags = append(tags, traceStateTags...)
	}
	return tags
}

func linksToDbSpanRefs(links ptrace.SpanLinkSlice, parentSpanID dbmodel.SpanID, traceID dbmodel.TraceID) []dbmodel.Reference {
	refsCount := links.Len()
	if parentSpanID != "" {
		refsCount++
	}

	refs := make([]dbmodel.Reference, 0, refsCount)

	if parentSpanID != "" {
		refs = append(refs, dbmodel.Reference{
			TraceID: traceID,
			SpanID:  parentSpanID,
			RefType: dbmodel.ChildOf,
		})
	}

	for i := 0; i < links.Len(); i++ {
		link := links.At(i)
		linkTraceID := dbmodel.TraceID(link.TraceID().String())
		linkSpanID := dbmodel.SpanID(link.SpanID().String())
		linkRefType := refTypeFromLink(link)
		if parentSpanID != "" && linkTraceID == traceID && linkSpanID == parentSpanID {
			refs[0].RefType = linkRefType
			continue
		}
		refs = append(refs, dbmodel.Reference{
			TraceID: linkTraceID,
			SpanID:  linkSpanID,
			RefType: linkRefType,
		})
	}

	return refs
}

func spanEventsToDbSpanLogs(events ptrace.SpanEventSlice) []dbmodel.Log {
	logs := make([]dbmodel.Log, 0, events.Len())
	for i := 0; i < events.Len(); i++ {
		event := events.At(i)
		fields := make([]dbmodel.KeyValue, 0, event.Attributes().Len()+1)
		_, eventAttrFound := event.Attributes().Get(eventNameAttr)
		if event.Name() != "" && !eventAttrFound {
			fields = append(fields, dbmodel.KeyValue{
				Key:   eventNameAttr,
				Type:  dbmodel.StringType,
				Value: event.Name(),
			})
		}
		fields = appendTagsFromAttributes(fields, event.Attributes())
		logs = append(logs, dbmodel.Log{
			Timestamp: model.TimeAsEpochMicroseconds(event.Timestamp().AsTime()),
			Fields:    fields,
		})
	}

	return logs
}

func getTagFromSpanKind(spanKind ptrace.SpanKind) (dbmodel.KeyValue, bool) {
	var tagStr string
	switch spanKind {
	case ptrace.SpanKindClient:
		tagStr = string(model.SpanKindClient)
	case ptrace.SpanKindServer:
		tagStr = string(model.SpanKindServer)
	case ptrace.SpanKindProducer:
		tagStr = string(model.SpanKindProducer)
	case ptrace.SpanKindConsumer:
		tagStr = string(model.SpanKindConsumer)
	case ptrace.SpanKindInternal:
		tagStr = string(model.SpanKindInternal)
	default:
		return dbmodel.KeyValue{}, false
	}

	return dbmodel.KeyValue{
		Key:   model.SpanKindKey,
		Type:  dbmodel.StringType,
		Value: tagStr,
	}, true
}

func getTagsFromStatusCode(statusCode ptrace.StatusCode) []dbmodel.KeyValue {
	switch statusCode {
	case ptrace.StatusCodeError:
		return []dbmodel.KeyValue{
			{Key: otelStatusCode, Type: dbmodel.StringType, Value: statusError},
			{Key: "error", Type: dbmodel.BoolType, Value: "true"},
		}
	case ptrace.StatusCodeOk:
		return []dbmodel.KeyValue{
			{Key: otelStatusCode, Type: dbmodel.StringType, Value: statusOk},
		}
	default:
		return nil
	}
}

func getTagFromStatusMsg(statusMsg string) (dbmodel.KeyValue, bool) {
	if statusMsg == "" {
		return dbmodel.KeyValue{}, false
	}
	return dbmodel.KeyValue{
		Key:   otelStatusDescription,
		Type:  dbmodel.StringType,
		Value: statusMsg,
	}, true
}

func getTagsFromTraceState(traceState string) ([]dbmodel.KeyValue, bool) {
	var keyValues []dbmodel.KeyValue
	exists := traceState != ""
	if exists {
		kv := dbmodel.KeyValue{
			Key:   tagW3CTraceState,
			Value: traceState,
			Type:  dbmodel.StringType,
		}
		keyValues = append(keyValues, kv)
	}
	return keyValues, exists
}

func getTagsFromInstrumentationLibrary(il pcommon.InstrumentationScope) ([]dbmodel.KeyValue, bool) {
	var keyValues []dbmodel.KeyValue
	if ilName := il.Name(); ilName != "" {
		kv := dbmodel.KeyValue{
			Key:   otelScopeName,
			Type:  dbmodel.StringType,
			Value: ilName,
		}
		keyValues = append(keyValues, kv)
	}
	if ilVersion := il.Version(); ilVersion != "" {
		kv := dbmodel.KeyValue{
			Key:   otelScopeVersion,
			Type:  dbmodel.StringType,
			Value: ilVersion,
		}
		keyValues = append(keyValues, kv)
	}
	return keyValues, len(keyValues) > 0
}

func refTypeFromLink(link ptrace.SpanLink) dbmodel.ReferenceType {
	refTypeAttr, ok := link.Attributes().Get(opentracingRefType)
	if !ok {
		return dbmodel.FollowsFrom
	}
	return strToDbSpanRefType(refTypeAttr.Str())
}

func strToDbSpanRefType(attr string) dbmodel.ReferenceType {
	if attr == opentracingRefTypeChildOf {
		return dbmodel.ChildOf
	}
	return dbmodel.FollowsFrom
}
