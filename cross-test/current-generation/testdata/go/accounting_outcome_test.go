// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for tacplus#273. NOT generated: run.sh copies this file into
// the accounting-sink fixture's generated AgentApi wrapper package before
// `go test ./...`.
//
// It drives the real generated wrapper with the real generated Emitter and
// EventBusSink, and reads the AccountingRecords the sink hands the publisher:
//   - a query that returns N records produces exactly one record, PERMITTED,
//     records_returned=N, and no request or response value;
//   - a denied call produces exactly one record, DENIED_AUTHZ with its reason
//     code, and no error text; an unauthenticated one DENIED_AUTHN;
//   - one user call produces exactly one record end to end: the call that
//     stores it carries the accounting_internal marker and, made by a service,
//     records nothing, while a user's call with the marker is still recorded.

package agentsensor

import (
	"bytes"
	"context"
	"testing"
	"time"

	"gitlab.com/example/dummy/agentsensor/tacplus/model/chassis/am"
	"gitlab.com/example/dummy/agentsensor/tacplus/model/chassis/servicemanager"
	wrapperToken "gitlab.com/example/dummy/agentsensor/tacplus/model/chassis/token"
	types "gitlab.com/example/dummy/agentsensor/tacplus/model/types/services/agentsensor"
	accounting_pb "gitlab.com/example/dummy/agentsensor/tacplus/model/types/sys/accounting"
	accountingrecord_pb "gitlab.com/example/dummy/agentsensor/tacplus/model/types/vservices/accountingrecord"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	secretFilter = "SECRET-FILTER-VALUE"
	secretID     = "SECRET-EVENT-ID"
	secretDenial = "SECRET-DENIAL-DETAIL"
	queryN       = 3
)

type queryHandler struct{}

func (queryHandler) Ping(context.Context, *servicemanager.Caller, *types.AgentEvent) (*types.AgentEvent, error) {
	return &types.AgentEvent{Id: secretID}, nil
}

func (queryHandler) GetAgentEvents(ctx context.Context, _ *servicemanager.Caller, req *types.GetAgentEventsRequest) (*types.GetAgentEventsResponse, error) {
	resp := &types.GetAgentEventsResponse{Total: queryN}
	for i := 0; i < queryN; i++ {
		resp.Events = append(resp.Events, &types.AgentEvent{Id: secretID, Body: []byte(req.Filter)})
	}
	am.SetRecordsReturned(ctx, int64(len(resp.Events)))
	return resp, nil
}

// accounted returns a wrapper whose auth check returns authErr, and the queue
// the accounting sink feeds.
func accounted(t *testing.T, authErr error) (*AgentApi, chan *accountingrecord_pb.AccountingRecord) {
	return accountedAs(t, nil, authErr)
}

// accountedAs is accounted with the token the auth check verifies.
func accountedAs(t *testing.T, tok *wrapperToken.TokenWrapper, authErr error) (*AgentApi, chan *accountingrecord_pb.AccountingRecord) {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	tx := make(chan *accountingrecord_pb.AccountingRecord, 16)
	w := NewAgentApi(queryHandler{})
	w.SetEmitter(am.NewEmitter("agentsensor-test", am.NewEventBusSink(tx, "agentsensor-test", stop)))
	w.SetAuthorizeFunc(func(context.Context, string, any) (*wrapperToken.TokenWrapper, error) {
		if authErr != nil {
			return nil, authErr
		}
		return tok, nil
	})
	return w, tx
}

// exactlyOne reads the one record a call produced and fails on a second.
func exactlyOne(t *testing.T, tx chan *accountingrecord_pb.AccountingRecord) *accountingrecord_pb.AccountingRecord {
	t.Helper()
	var rec *accountingrecord_pb.AccountingRecord
	select {
	case rec = <-tx:
	case <-time.After(2 * time.Second):
		t.Fatal("the call produced no accounting record")
	}
	select {
	case extra := <-tx:
		t.Fatalf("one call produced a second accounting record: %v", extra)
	case <-time.After(200 * time.Millisecond):
	}
	return rec
}

// noData fails when any value the call carried reaches the record.
func noData(t *testing.T, rec *accountingrecord_pb.AccountingRecord) {
	t.Helper()
	raw, err := proto.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{secretFilter, secretID, secretDenial} {
		if bytes.Contains(raw, []byte(v)) {
			t.Fatalf("accounting record carries data %q: %v", v, rec)
		}
	}
}

func TestAccountingQueryRecordsOutcomeAndRecordsReturned(t *testing.T) {
	w, tx := accounted(t, nil)
	resp, err := w.GetAgentEvents(context.Background(), &types.GetAgentEventsRequest{Filter: secretFilter})
	if err != nil || len(resp.GetEvents()) != queryN {
		t.Fatalf("query: %v, %d events", err, len(resp.GetEvents()))
	}
	rec := exactlyOne(t, tx)
	if rec.Outcome != accounting_pb.RpcOutcome_RPC_OUTCOME_PERMITTED || rec.Code != 0 ||
		rec.Reason != accounting_pb.RpcReason_RPC_REASON_UNDEFINED || rec.RecordsReturned != queryN {
		t.Fatalf("query record: want PERMITTED, code 0, no reason, records_returned=%d; got %v", queryN, rec)
	}
	if rec.Method != "GetAgentEvents" || rec.Level != accounting_pb.EventLevel_EVENT_LEVEL_INFO {
		t.Fatalf("query record: method %q level %v", rec.Method, rec.Level)
	}
	noData(t, rec)
}

func TestAccountingDeniedCallRecordsReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		authErr error
		outcome accounting_pb.RpcOutcome
		reason  accounting_pb.RpcReason
		code    codes.Code
	}{
		{"authz", status.Error(codes.PermissionDenied, secretDenial),
			accounting_pb.RpcOutcome_RPC_OUTCOME_DENIED_AUTHZ, accounting_pb.RpcReason_RPC_REASON_NOT_ENTITLED, codes.PermissionDenied},
		{"authn", status.Error(codes.Unauthenticated, secretDenial),
			accounting_pb.RpcOutcome_RPC_OUTCOME_DENIED_AUTHN, accounting_pb.RpcReason_RPC_REASON_TOKEN_REJECTED, codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, tx := accounted(t, tc.authErr)
			if _, err := w.GetAgentEvents(context.Background(), &types.GetAgentEventsRequest{Filter: secretFilter}); status.Code(err) != tc.code {
				t.Fatalf("denied call returned %v, want %v", err, tc.code)
			}
			rec := exactlyOne(t, tx)
			if rec.Outcome != tc.outcome || rec.Reason != tc.reason || rec.Code != int32(tc.code) ||
				rec.Level != accounting_pb.EventLevel_EVENT_LEVEL_WARN || rec.RecordsReturned != 0 {
				t.Fatalf("denied record: want %v %v code %d WARN; got %v", tc.outcome, tc.reason, tc.code, rec)
			}
			noData(t, rec)
		})
	}
}

func token(subType, sub string) *wrapperToken.TokenWrapper {
	return wrapperToken.NewImmutableToken(nil).SetSubType(subType).SetSubject(sub)
}

// noRecord fails when a call produced any accounting record.
func noRecord(t *testing.T, tx chan *accountingrecord_pb.AccountingRecord) {
	t.Helper()
	select {
	case rec := <-tx:
		t.Fatalf("a call that stores accounting produced an accounting record: %v", rec)
	case <-time.After(300 * time.Millisecond):
	}
}

// The no-recursion requirement of tacplus#273: a user call is recorded once,
// and the call that carries its record onward (a service, with the marker the
// generated accounting publisher sends) is not recorded, so the record does not
// amplify. The marker on a user's own call changes nothing.
func TestAccountingOneCallOneRecordEndToEnd(t *testing.T) {
	user, userTx := accountedAs(t, token("user", "alice"), nil)
	if _, err := user.GetAgentEvents(context.Background(), &types.GetAgentEventsRequest{Filter: secretFilter}); err != nil {
		t.Fatal(err)
	}
	rec := exactlyOne(t, userTx)
	if rec.PrincipalSub != "alice" || rec.Outcome != accounting_pb.RpcOutcome_RPC_OUTCOME_PERMITTED {
		t.Fatalf("user call record: %v", rec)
	}

	internal := metadata.NewIncomingContext(context.Background(), metadata.Pairs(am.AccountingInternalHeader, "1"))
	store, storeTx := accountedAs(t, token("service", "stratum"), nil)
	if _, err := store.GetAgentEvents(internal, &types.GetAgentEventsRequest{}); err != nil {
		t.Fatal(err)
	}
	noRecord(t, storeTx)

	evader, evaderTx := accountedAs(t, token("user", "mallory"), nil)
	if _, err := evader.GetAgentEvents(internal, &types.GetAgentEventsRequest{}); err != nil {
		t.Fatal(err)
	}
	if rec := exactlyOne(t, evaderTx); rec.PrincipalSub != "mallory" {
		t.Fatalf("a user's call with the marker must still be recorded as theirs: %v", rec)
	}
}
