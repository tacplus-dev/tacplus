// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func acctSafeAnn() map[string]model.AnnotationValue {
	return map[string]model.AnnotationValue{"accounting_safe": {Type: model.AnnotationTypeBool, BoolValue: true}}
}

// accountingFiles is the shape of sys/accounting.proto and a declared
// AccountingRecord: identifiers marked (spec.accounting_safe), numbers and
// enums, a config message no event reaches, and the injected trace_id.
func accountingFiles() []model.ProtoFile {
	return []model.ProtoFile{
		{Package: "accounting", Messages: []model.Message{
			{Name: "Event", Fields: []model.Field{
				{Name: "request_id", Type: "string", Number: 3, Annotations: acctSafeAnn()},
				{Name: "identity", Number: 4, IsMap: true, MapKeyType: "string", MapValueType: "string", Annotations: acctSafeAnn()},
				{Name: "level", Type: "EventLevel", Number: 5, IsEnum: true},
				{Name: "rpc_completed", Type: "RpcCompleted", Number: 10, IsMessage: true},
				{Name: "trace_id", Type: "string", Number: 200},
			}},
			{Name: "RpcCompleted", Fields: []model.Field{
				{Name: "duration_ms", Type: "int64", Number: 2},
				{Name: "metrics", Type: "RpcMetrics", Number: 6, IsMessage: true},
			}},
			{Name: "RpcMetrics", Fields: []model.Field{
				{Name: "records_returned", Type: "int64", Number: 1},
			}},
			// Not reachable from Event: configuration, not an event.
			{Name: "SpoolConfig", Fields: []model.Field{
				{Name: "spool_path", Type: "string", Number: 3},
			}},
		}},
		{Package: "accountingrecord", Messages: []model.Message{
			{Name: "AccountingRecord", Fields: []model.Field{
				{Name: "accounting_id", Type: "string", Number: 1, Annotations: acctSafeAnn()},
				{Name: "records_returned", Type: "int64", Number: 12},
			}},
		}},
	}
}

func TestAccountingSafe_DeclaredShapePasses(t *testing.T) {
	if err := ValidateAccountingSafe(accountingFiles()); err != nil {
		t.Fatalf("a proto set with every accounting string marked must pass: %v", err)
	}
}

// The acceptance check of tacplus#273: a planted free-text field in an
// accounting event fails generation, by name.
func TestAccountingSafe_RejectsPlantedFreeText(t *testing.T) {
	cases := map[string]func(f []model.ProtoFile){
		"accounting.RpcCompleted.message": func(f []model.ProtoFile) {
			f[0].Messages[1].Fields = append(f[0].Messages[1].Fields, model.Field{Name: "message", Type: "string", Number: 7})
		},
		"accounting.RpcMetrics.payload": func(f []model.ProtoFile) {
			f[0].Messages[2].Fields = append(f[0].Messages[2].Fields, model.Field{Name: "payload", Type: "bytes", Number: 9})
		},
		"accounting.Event.labels": func(f []model.ProtoFile) {
			f[0].Messages[0].Fields = append(f[0].Messages[0].Fields, model.Field{Name: "labels", Number: 8, IsMap: true, MapKeyType: "string", MapValueType: "int64"})
		},
		"accountingrecord.AccountingRecord.error_message": func(f []model.ProtoFile) {
			f[1].Messages[0].Fields = append(f[1].Messages[0].Fields, model.Field{Name: "error_message", Type: "string", Number: 13, IsRepeated: true})
		},
	}
	for want, plant := range cases {
		files := accountingFiles()
		plant(files)
		err := ValidateAccountingSafe(files)
		if err == nil {
			t.Fatalf("%s: a free-text field in an accounting event must fail generation", want)
		}
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "(spec.accounting_safe)") {
			t.Fatalf("%s: the error must name the field and the annotation, got: %v", want, err)
		}
	}
}

func TestAccountingSafe_RefusedOnNonStringField(t *testing.T) {
	files := accountingFiles()
	files[0].Messages[1].Fields[0].Annotations = acctSafeAnn() // duration_ms, an int64
	err := ValidateAccountingSafe(files)
	if err == nil || !strings.Contains(err.Error(), "accounting.RpcCompleted.duration_ms") {
		t.Fatalf("(spec.accounting_safe) on a number must be refused, got: %v", err)
	}
}

func TestAccountingSafe_NoAccountingTypesNoGate(t *testing.T) {
	files := []model.ProtoFile{{Package: "app", Messages: []model.Message{
		{Name: "Event", Fields: []model.Field{{Name: "text", Type: "string", Number: 1}}},
	}}}
	if err := ValidateAccountingSafe(files); err != nil {
		t.Fatalf("a message outside the accounting types is not gated: %v", err)
	}
}
