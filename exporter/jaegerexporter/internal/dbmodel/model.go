// Copyright (c) 2019 The Jaeger Authors.
// SPDX-License-Identifier: Apache-2.0
//
// Adapted from github.com/jaegertracing/jaeger
// internal/storage/v2/elasticsearch/tracestore/core/dbmodel.

package dbmodel

type ReferenceType string

type TraceID string

type SpanID string

type ValueType string

const (
	ChildOf     ReferenceType = "CHILD_OF"
	FollowsFrom ReferenceType = "FOLLOWS_FROM"

	StringType  ValueType = "string"
	BoolType    ValueType = "bool"
	Int64Type   ValueType = "int64"
	Float64Type ValueType = "float64"
	BinaryType  ValueType = "binary"
)

type Span struct {
	TraceID         TraceID        `json:"traceID"`
	SpanID          SpanID         `json:"spanID"`
	ParentSpanID    SpanID         `json:"parentSpanID,omitempty"`
	Flags           uint32         `json:"flags,omitempty"`
	OperationName   string         `json:"operationName"`
	References      []Reference    `json:"references"`
	StartTime       uint64         `json:"startTime"`
	StartTimeMillis uint64         `json:"startTimeMillis"`
	Duration        uint64         `json:"duration"`
	Tags            []KeyValue     `json:"tags"`
	Tag             map[string]any `json:"tag,omitempty"`
	Logs            []Log          `json:"logs"`
	Process         Process        `json:"process"`
	Timestamp       string         `json:"@timestamp,omitempty"`
}

type Reference struct {
	RefType ReferenceType `json:"refType"`
	TraceID TraceID       `json:"traceID"`
	SpanID  SpanID        `json:"spanID"`
}

type Process struct {
	ServiceName string         `json:"serviceName"`
	Tags        []KeyValue     `json:"tags"`
	Tag         map[string]any `json:"tag,omitempty"`
}

type Log struct {
	Timestamp uint64     `json:"timestamp"`
	Fields    []KeyValue `json:"fields"`
}

type KeyValue struct {
	Key   string    `json:"key"`
	Type  ValueType `json:"type,omitempty"`
	Value any       `json:"value"`
}

type Service struct {
	ServiceName   string `json:"serviceName"`
	OperationName string `json:"operationName"`
}
