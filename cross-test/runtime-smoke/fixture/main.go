// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// Runtime-smoke fixture server: hosts the generated go servers for the
// consumer-contract fixture so python/typescript generated CLIENTS can
// exercise them over localhost. This recovers the runtime-smoke intent of the
// pre-PAR-3 python/typescript `both` consumers, which hosted python/TS s2sm
// servers in-process — dead under the full-client taxonomy (python/TS emit no
// service runtime). The server half of those smokes now lives here, in go.
//
// Surfaces hosted:
//   - C2S gRPC (real generated ContractApi wrapper + auth funcs + validation)
//   - C2S gRPC "unwired" (mandatory-auth wrapper with NO auth funcs — clients
//     assert the fail-closed rejection over the wire)
//   - ContractAuditApi gRPC with a recording accounting Emitter and trace
//     publisher (clients drive RPCs, then assert counts via /fixture/stats)
//   - W2S HTTP/JSON + SSE + WebSocket endpoints implementing the W2S wire
//     protocol (tacplus emits no W2S server; this hand-written implementation
//     mirrors the protocol asserted by the go w2sm client tests in
//     cross-test/consumer-contract/run.sh)
//   - GET /fixture/stats: JSON counters so clients can assert handler-reached
//     / handler-not-reached and accounting/trace expectations over the wire
//
// Startup contract: prints a single JSON line to stdout
//
//	{"grpc":"127.0.0.1:N","grpcUnwired":"127.0.0.1:N","http":"http://127.0.0.1:N"}
//
// then serves until SIGINT/SIGTERM.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	am "gitlab.com/example/tacplus/consumer/contractclient/model/chassis/am"
	"gitlab.com/example/tacplus/consumer/contractclient/model/chassis/collection"
	"gitlab.com/example/tacplus/consumer/contractclient/model/chassis/servicemanager"
	wrapperToken "gitlab.com/example/tacplus/consumer/contractclient/model/chassis/token"
	server "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm/grpc/servers/contract/contractclient"
	auditserver "gitlab.com/example/tacplus/consumer/contractclient/model/s2sm/grpc/servers/services/contractclient"
	types "gitlab.com/example/tacplus/consumer/contractclient/model/types/contract/contractclient"
	audittypes "gitlab.com/example/tacplus/consumer/contractclient/model/types/services/contractclient"
	accounting_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/sys/accounting"
	trace_pb "gitlab.com/example/tacplus/consumer/contractclient/model/types/sys/trace"
	"golang.org/x/net/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const bearerToken = "Bearer token"

// The identity the auth funcs verify. Every authenticated RPC must hand the
// handler a caller carrying exactly this subject: the generated wrapper builds
// the caller from the token the auth check returned, so a nil caller (or a
// different subject) on an authenticated RPC means that plumbing is broken.
const (
	fixtureIssuer   = "https://tokenexchanger.example.org"
	fixtureSubject  = "spiffe://example.org/ns/contract/sa/fixture-caller"
	fixtureAudience = "contractclient"
)

// verifiedToken is what the fixture's auth funcs return on success, standing in
// for the token a real Authorizer verifies off the bearer header.
var verifiedToken = mustFixtureToken()

func mustFixtureToken() *wrapperToken.TokenWrapper {
	part := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	jwt := part(map[string]string{"alg": "EdDSA", "kid": "fixture-kid"}) + "." +
		part(map[string]string{"iss": fixtureIssuer, "sub": fixtureSubject, "aud": fixtureAudience}) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("fixture-signature"))
	tok, err := wrapperToken.ParseToken(jwt)
	if err != nil {
		panic(err)
	}
	return tok
}

// stats is the observable server-side state. Consumers fetch it via
// /fixture/stats to assert handler-reached counts (e.g. an unauthenticated
// call must NOT reach the handler) and accounting/trace behavior — the parts
// of the recovered spec that were previously asserted in-process.
type stats struct {
	mu sync.Mutex

	ReadCalls           int `json:"readCalls"`
	PublicReadCalls     int `json:"publicReadCalls"`
	ListEventsCalls     int `json:"listEventsCalls"`
	WatchCalls          int `json:"watchCalls"`
	PagedCalls          int `json:"pagedCalls"`
	AuthorizeCalls      int `json:"authorizeCalls"`
	AuthenticateCalls   int `json:"authenticateCalls"`
	UnwiredHandlerCalls int `json:"unwiredHandlerCalls"`

	W2SPublicReadRequests int `json:"w2sPublicReadRequests"`
	W2SReadRequests       int `json:"w2sReadRequests"`
	W2SWatchRequests      int `json:"w2sWatchRequests"`
	W2SListEventsRequests int `json:"w2sListEventsRequests"`
	W2SPagedRequests      int `json:"w2sPagedRequests"`
	W2SUploadConnections  int `json:"w2sUploadConnections"`
	W2SChatConnections    int `json:"w2sChatConnections"`

	// Wire-protocol violations observed server-side (wrong method, missing or
	// unexpected authorization, malformed frames). Consumers assert this stays
	// empty — it is the server-side half of every client-behavior assertion.
	Violations []string `json:"violations"`

	AccountingCompleted map[string]int         `json:"accountingCompleted"`
	AccountingErrors    map[string]int         `json:"accountingErrors"`
	Traces              map[string]*traceEntry `json:"traces"`
}

type traceEntry struct {
	Kinds         []string `json:"kinds"`
	ResponseRPC   string   `json:"responseRpc"`
	ResponseError string   `json:"responseError"`
}

func newStats() *stats {
	return &stats{
		AccountingCompleted: map[string]int{},
		AccountingErrors:    map[string]int{},
		Traces:              map[string]*traceEntry{},
	}
}

func (s *stats) bump(field *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*field++
}

// requireCaller records a violation unless the handler received the caller the
// auth check verified. This is the runtime half of the "verified identity
// reaches the handler" contract — a wrapper that rebuilt the caller from
// anything other than the auth call's own result would trip it.
func (s *stats) requireCaller(rpc string, caller *servicemanager.Caller) {
	if caller == nil {
		s.violation("%s: handler received a nil caller on an authenticated rpc", rpc)
		return
	}
	if got := caller.Token().GetSubject(); got != fixtureSubject {
		s.violation("%s: handler caller subject %q, want %q", rpc, got, fixtureSubject)
	}
}

// requireNoCaller is the inverse: an auth-disabled rpc verifies no token, so
// its handler must see no caller at all.
func (s *stats) requireNoCaller(rpc string, caller *servicemanager.Caller) {
	if caller != nil {
		s.violation("%s: handler received a caller on an auth-disabled rpc", rpc)
	}
}

func (s *stats) violation(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Violations = append(s.Violations, fmt.Sprintf(format, args...))
}

func (s *stats) snapshotJSON() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Marshal(s)
}

// --- accounting / trace recording ---

type accountingSink struct{ s *stats }

func (a accountingSink) Emit(_ context.Context, event *accounting_pb.Event) error {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if rpc := event.GetRpcCompleted(); rpc != nil {
		a.s.AccountingCompleted[rpc.GetMethod().GetContractclient().String()]++
		return nil
	}
	if rpc := event.GetRpcError(); rpc != nil {
		a.s.AccountingErrors[rpc.GetMethod().GetContractclient().String()]++
	}
	return nil
}

func (s *stats) recordTrace(event *trace_pb.TraceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.Traces[event.GetTraceId()]
	if entry == nil {
		entry = &traceEntry{}
		s.Traces[event.GetTraceId()] = entry
	}
	entry.Kinds = append(entry.Kinds, event.GetKind().String())
	if event.GetKind() == trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_RESPONSE {
		entry.ResponseRPC = event.GetRpc().GetContractclient().String()
		entry.ResponseError = event.GetError()
	}
}

// --- gRPC handlers (mirror the recovered python ApiHandler semantics) ---

func validResponse() *types.ContractResponse {
	return &types.ContractResponse{ResultId: "result-123", Accepted: true}
}

func validEvent() *types.ContractEvent {
	return &types.ContractEvent{EventId: "event-1", TenantNamespace: "tenant.alpha", Payload: "ok"}
}

func invalidEvent() *types.ContractEvent {
	return &types.ContractEvent{TenantNamespace: "tenant.alpha", Payload: "missing-id"}
}

func singleEventSeq(event *types.ContractEvent) iter.Seq2[*types.ContractEvent, error] {
	return func(yield func(*types.ContractEvent, error) bool) {
		_ = yield(event, nil)
	}
}

func emptyWatchChangeSeq() iter.Seq2[*types.ContractWatchChange, error] {
	return func(yield func(*types.ContractWatchChange, error) bool) {}
}

func watchSource(event *types.ContractEvent) *collection.WatchSource[types.ContractEvent, types.ContractWatchChange] {
	return &collection.WatchSource[types.ContractEvent, types.ContractWatchChange]{
		Scope:    "tenant.alpha",
		Snapshot: singleEventSeq(event),
		Revision: 1,
		Changes:  emptyWatchChangeSeq,
	}
}

type apiHandler struct{ s *stats }

func (h apiHandler) GetContract(_ context.Context, caller *servicemanager.Caller, _ *types.ContractRequest) (*types.ContractResponse, error) {
	h.s.bump(&h.s.ReadCalls)
	h.s.requireCaller("GetContract", caller)
	return validResponse(), nil
}

func (h apiHandler) GetPublicContract(_ context.Context, caller *servicemanager.Caller, req *types.ContractRequest) (*types.ContractResponse, error) {
	h.s.bump(&h.s.PublicReadCalls)
	h.s.requireNoCaller("GetPublicContract", caller)
	if req.GetItemId() == "bad-response" {
		return &types.ContractResponse{Accepted: true}, nil
	}
	return validResponse(), nil
}

func (h apiHandler) ListEvents(caller *servicemanager.Caller, req *types.ContractRequest) (iter.Seq2[*types.ContractEvent, error], error) {
	h.s.bump(&h.s.ListEventsCalls)
	h.s.requireCaller("ListEvents", caller)
	if req.GetItemId() == "bad-event" {
		return singleEventSeq(invalidEvent()), nil
	}
	return singleEventSeq(validEvent()), nil
}

func (h apiHandler) ListEventsPaged(_ context.Context, caller *servicemanager.Caller, _ *types.ContractPageRequest, page collection.PageParams) (collection.Page[*types.ContractEvent], error) {
	h.s.bump(&h.s.PagedCalls)
	h.s.requireCaller("ListEventsPaged", caller)
	// TWO keyset pages (PC-C-3): the first call (nil After) returns event-1 plus
	// a resume cursor, forcing auto-pagers through the token-CONTINUATION branch
	// — a second fetch with a NON-EMPTY decoded token — before the second page
	// terminates the walk (nil NextAfter -> empty next_page_token).
	if page.After == nil {
		return collection.Page[*types.ContractEvent]{
			Items:     []*types.ContractEvent{validEvent()},
			NextAfter: &collection.Cursor{Keys: []string{"event-1"}},
			Total:     2,
		}, nil
	}
	if len(page.After.Keys) != 1 || page.After.Keys[0] != "event-1" {
		return collection.Page[*types.ContractEvent]{}, status.Errorf(codes.InvalidArgument, "unexpected resume cursor %v", page.After.Keys)
	}
	return collection.Page[*types.ContractEvent]{
		Items: []*types.ContractEvent{{EventId: "event-2", TenantNamespace: "tenant.alpha", Payload: "ok"}},
		Total: 2,
	}, nil
}

func (h apiHandler) Watch(caller *servicemanager.Caller, req *types.ContractWatchRequest) (*collection.WatchSource[types.ContractEvent, types.ContractWatchChange], error) {
	h.s.bump(&h.s.WatchCalls)
	h.s.requireCaller("Watch", caller)
	if req.GetItemId() == "bad-event" {
		return watchSource(invalidEvent()), nil
	}
	return watchSource(validEvent()), nil
}

func (h apiHandler) SendEvents(caller *servicemanager.Caller, stream grpc.ClientStreamingServer[types.ContractEvent, types.ContractResponse]) error {
	h.s.requireCaller("SendEvents", caller)
	for {
		if _, err := stream.Recv(); err == io.EOF {
			return stream.SendAndClose(validResponse())
		} else if err != nil {
			return err
		}
	}
}

func (h apiHandler) AssistEvents(caller *servicemanager.Caller, stream grpc.BidiStreamingServer[types.ContractEvent, types.ContractEvent]) error {
	h.s.requireCaller("AssistEvents", caller)
	for {
		if _, err := stream.Recv(); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

// unwiredHandler backs the fail-closed server: any handler call is a violation
// because the mandatory-auth wrapper has no auth funcs wired.
type unwiredHandler struct {
	apiHandler
	s *stats
}

func (h unwiredHandler) GetContract(context.Context, *servicemanager.Caller, *types.ContractRequest) (*types.ContractResponse, error) {
	h.s.bump(&h.s.UnwiredHandlerCalls)
	return validResponse(), nil
}

type auditHandler struct{ s *stats }

func (h auditHandler) ExecuteAudit(_ context.Context, caller *servicemanager.Caller, req *audittypes.AuditRequest) (*audittypes.AuditResponse, error) {
	h.s.requireNoCaller("ExecuteAudit", caller)
	if req.GetItemId() == "err" {
		return nil, status.Error(codes.Internal, "audit unary failed")
	}
	return &audittypes.AuditResponse{ResultId: "audit-result"}, nil
}

func (h auditHandler) GenerateAuditEvents(caller *servicemanager.Caller, req *audittypes.AuditRequest, stream grpc.ServerStreamingServer[audittypes.AuditEvent]) error {
	h.s.requireNoCaller("GenerateAuditEvents", caller)
	if req.GetItemId() == "err" {
		return status.Error(codes.Internal, "audit stream failed")
	}
	return stream.Send(&audittypes.AuditEvent{EventId: "audit-event"})
}

func (h auditHandler) SendAuditEvents(caller *servicemanager.Caller, stream grpc.ClientStreamingServer[audittypes.AuditEvent, audittypes.AuditResponse]) error {
	h.s.requireNoCaller("SendAuditEvents", caller)
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&audittypes.AuditResponse{ResultId: "audit-sendEvents"})
		}
		if err != nil {
			return err
		}
		if event.GetEventId() == "err" {
			return status.Error(codes.Internal, "audit sendEvents failed")
		}
	}
}

func (h auditHandler) AssistAuditEvents(caller *servicemanager.Caller, stream grpc.BidiStreamingServer[audittypes.AuditEvent, audittypes.AuditEvent]) error {
	h.s.requireNoCaller("AssistAuditEvents", caller)
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if event.GetEventId() == "err" {
			return status.Error(codes.Internal, "audit assistEvents failed")
		}
		if err := stream.Send(&audittypes.AuditEvent{EventId: "audit-reply"}); err != nil {
			return err
		}
	}
}

// --- auth funcs (mirror the go both-consumer runtime smoke) ---

func requireBearer(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get("authorization")) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization header")
	}
	if got := md.Get("authorization")[0]; got != bearerToken {
		return status.Errorf(codes.Unauthenticated, "unexpected authorization header %q", got)
	}
	return nil
}

// Each auth func returns the token it verified; the wrapper turns that token
// into the caller it hands the handler.
func wireAuth(wrapper interface {
	SetAuthorizeFunc(func(context.Context, string, any) (*wrapperToken.TokenWrapper, error))
	SetAuthenticateFunc(func(context.Context) (*wrapperToken.TokenWrapper, error))
	SetAuthenticateAtOrBelowFunc(func(context.Context, string) (*wrapperToken.TokenWrapper, error))
}, s *stats) {
	wrapper.SetAuthorizeFunc(func(ctx context.Context, _ string, req any) (*wrapperToken.TokenWrapper, error) {
		s.bump(&s.AuthorizeCalls)
		if err := requireBearer(ctx); err != nil {
			return nil, err
		}
		typed, ok := req.(*types.ContractRequest)
		if !ok || typed == nil {
			return nil, status.Error(codes.Internal, "authorize did not receive the request message")
		}
		if typed.GetTenantNamespace() != "tenant.alpha" {
			return nil, status.Error(codes.PermissionDenied, "caller namespace is not at-or-above target")
		}
		return verifiedToken, nil
	})
	wrapper.SetAuthenticateFunc(func(ctx context.Context) (*wrapperToken.TokenWrapper, error) {
		s.bump(&s.AuthenticateCalls)
		if err := requireBearer(ctx); err != nil {
			return nil, err
		}
		return verifiedToken, nil
	})
	wrapper.SetAuthenticateAtOrBelowFunc(func(ctx context.Context, targetNamespace string) (*wrapperToken.TokenWrapper, error) {
		s.bump(&s.AuthenticateCalls)
		if err := requireBearer(ctx); err != nil {
			return nil, err
		}
		if targetNamespace != "/" && targetNamespace != "" {
			return nil, status.Errorf(codes.PermissionDenied, "unexpected target namespace %q", targetNamespace)
		}
		return verifiedToken, nil
	})
}

// --- W2S HTTP server (hand-written: tacplus emits no W2S server runtime) ---

func resultEnvelope(msg proto.Message) ([]byte, error) {
	payload, err := protojson.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(`{"result":`), payload...), '}'), nil
}

func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	_, _ = w.Write(body)
}

type w2sServer struct{ s *stats }

func (srv w2sServer) decodeBody(w http.ResponseWriter, r *http.Request, msg proto.Message) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		srv.s.violation("W2S %s: read body: %v", r.URL.Path, err)
		writeJSONError(w, http.StatusInternalServerError, "read body")
		return false
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, msg); err != nil {
		srv.s.violation("W2S %s: decode body: %v", r.URL.Path, err)
		writeJSONError(w, http.StatusBadRequest, "decode body")
		return false
	}
	return true
}

func (srv w2sServer) requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		srv.s.violation("W2S %s: unexpected method %s", r.URL.Path, r.Method)
		writeJSONError(w, http.StatusMethodNotAllowed, "unexpected method")
		return false
	}
	return true
}

func (srv w2sServer) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if got := r.Header.Get("authorization"); got != bearerToken {
		srv.s.violation("W2S %s: unexpected authorization header %q", r.URL.Path, got)
		writeJSONError(w, http.StatusUnauthorized, "unexpected authorization header")
		return false
	}
	return true
}

func (srv w2sServer) requireNoAuth(w http.ResponseWriter, r *http.Request) bool {
	if got := r.Header.Get("authorization"); got != "" {
		srv.s.violation("W2S %s: auth-disabled route received authorization %q", r.URL.Path, got)
		writeJSONError(w, http.StatusBadRequest, "unexpected authorization header")
		return false
	}
	return true
}

func (srv w2sServer) writeResult(w http.ResponseWriter, msg proto.Message) {
	body, err := resultEnvelope(msg)
	if err != nil {
		srv.s.violation("W2S marshal result: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "marshal result")
		return
	}
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write(body)
}

func (srv w2sServer) handlePublicRead(w http.ResponseWriter, r *http.Request) {
	if !srv.requirePost(w, r) || !srv.requireNoAuth(w, r) {
		return
	}
	var req types.ContractRequest
	if !srv.decodeBody(w, r, &req) {
		return
	}
	if err := types.ValidateContractRequest(&req); err != nil {
		srv.s.violation("W2S %s: invalid request reached server: %v", r.URL.Path, err)
		writeJSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	srv.s.bump(&srv.s.W2SPublicReadRequests)
	if req.GetItemId() == "bad-response" {
		srv.writeResult(w, &types.ContractResponse{Accepted: true})
		return
	}
	srv.writeResult(w, validResponse())
}

func (srv w2sServer) handleRead(w http.ResponseWriter, r *http.Request) {
	if !srv.requirePost(w, r) || !srv.requireAuth(w, r) {
		return
	}
	var req types.ContractRequest
	if !srv.decodeBody(w, r, &req) {
		return
	}
	srv.s.bump(&srv.s.W2SReadRequests)
	srv.writeResult(w, validResponse())
}

func (srv w2sServer) handlePaged(w http.ResponseWriter, r *http.Request) {
	if !srv.requirePost(w, r) || !srv.requireAuth(w, r) {
		return
	}
	var req types.ContractPageRequest
	if !srv.decodeBody(w, r, &req) {
		return
	}
	srv.s.bump(&srv.s.W2SPagedRequests)
	srv.writeResult(w, &types.ContractPageResponse{
		Items:      []*types.ContractEvent{validEvent()},
		TotalCount: 1,
	})
}

func (srv w2sServer) sseWrite(w http.ResponseWriter, msg proto.Message) {
	payload, err := resultEnvelope(msg)
	if err != nil {
		srv.s.violation("W2S SSE marshal: %v", err)
		return
	}
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(payload)
	_, _ = w.Write([]byte("\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (srv w2sServer) requireSSE(w http.ResponseWriter, r *http.Request) bool {
	if got := r.Header.Get("accept"); got != "text/event-stream" {
		srv.s.violation("W2S %s: unexpected accept header %q", r.URL.Path, got)
		writeJSONError(w, http.StatusBadRequest, "unexpected accept header")
		return false
	}
	return true
}

func (srv w2sServer) handleWatch(w http.ResponseWriter, r *http.Request) {
	if !srv.requirePost(w, r) || !srv.requireSSE(w, r) || !srv.requireAuth(w, r) {
		return
	}
	var req types.ContractWatchRequest
	if !srv.decodeBody(w, r, &req) {
		return
	}
	if err := types.ValidateContractWatchRequest(&req); err != nil {
		srv.s.violation("W2S %s: invalid request reached server: %v", r.URL.Path, err)
		writeJSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	srv.s.bump(&srv.s.W2SWatchRequests)
	w.Header().Set("content-type", "text/event-stream")
	if req.GetItemId() == "bad-event" {
		srv.sseWrite(w, &types.ContractWatchEvent{Event: &types.ContractWatchEvent_SnapshotItem{SnapshotItem: invalidEvent()}})
		return
	}
	srv.sseWrite(w, &types.ContractWatchEvent{Event: &types.ContractWatchEvent_SnapshotItem{SnapshotItem: validEvent()}})
	srv.sseWrite(w, &types.ContractWatchEvent{Event: &types.ContractWatchEvent_SnapshotDone{SnapshotDone: 1}})
}

func (srv w2sServer) handleListEvents(w http.ResponseWriter, r *http.Request) {
	if !srv.requirePost(w, r) || !srv.requireSSE(w, r) || !srv.requireAuth(w, r) {
		return
	}
	var req types.ContractRequest
	if !srv.decodeBody(w, r, &req) {
		return
	}
	srv.s.bump(&srv.s.W2SListEventsRequests)
	w.Header().Set("content-type", "text/event-stream")
	if req.GetItemId() == "bad-event" {
		srv.sseWrite(w, invalidEvent())
		return
	}
	srv.sseWrite(w, validEvent())
}

// wsHandler wraps a websocket handler WITHOUT x/net's default Origin check.
// websocket.Handler 403s any handshake lacking a browser Origin header — but
// the generated python/typescript W2S clients (like any non-browser client)
// send none, and the real gateway accepts origin-less upgrades. Auth lives in
// the frame protocol (start-frame authorization), not the handshake.
func wsHandler(h func(*websocket.Conn)) websocket.Server {
	return websocket.Server{
		Handler:   h,
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
	}
}

// W2S WebSocket frame protocol (client -> server):
//
//	{"type":"start","authorization":"Bearer <token>"}
//	{"type":"message","message":{...},"authorization":"Bearer <token>"}
//	{"type":"complete"}
//
// server -> client responses are bare result envelopes: {"result":{...}}.
type wsFrame struct {
	Type          string          `json:"type"`
	Authorization string          `json:"authorization"`
	Message       json.RawMessage `json:"message"`
}

func (srv w2sServer) recvFrame(ws *websocket.Conn, path string) (*wsFrame, error) {
	var raw string
	if err := websocket.Message.Receive(ws, &raw); err != nil {
		return nil, err
	}
	var frame wsFrame
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		srv.s.violation("W2S %s: undecodable frame %q: %v", path, raw, err)
		return nil, err
	}
	return &frame, nil
}

func (srv w2sServer) requireStart(ws *websocket.Conn, path string) bool {
	frame, err := srv.recvFrame(ws, path)
	if err != nil {
		srv.s.violation("W2S %s: missing start frame: %v", path, err)
		return false
	}
	if frame.Type != "start" || frame.Authorization != bearerToken {
		srv.s.violation("W2S %s: bad start frame type=%q auth=%q", path, frame.Type, frame.Authorization)
		return false
	}
	return true
}

func (srv w2sServer) decodeEventFrame(frame *wsFrame, path string) *types.ContractEvent {
	if frame.Authorization != bearerToken {
		srv.s.violation("W2S %s: message frame missing bearer auth (%q)", path, frame.Authorization)
		return nil
	}
	var event types.ContractEvent
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(frame.Message, &event); err != nil {
		srv.s.violation("W2S %s: decode message frame: %v", path, err)
		return nil
	}
	if err := types.ValidateContractEvent(&event); err != nil {
		srv.s.violation("W2S %s: invalid event reached server: %v", path, err)
		return nil
	}
	return &event
}

func (srv w2sServer) handleUpload(ws *websocket.Conn) {
	const path = "/contractclient/ContractApi/SendEvents"
	srv.s.bump(&srv.s.W2SUploadConnections)
	if got := ws.Request().URL.Path; got != path {
		srv.s.violation("W2S upload: unexpected websocket path %s", got)
		return
	}
	if !srv.requireStart(ws, path) {
		return
	}
	for {
		frame, err := srv.recvFrame(ws, path)
		if err != nil {
			srv.s.violation("W2S %s: recv: %v", path, err)
			return
		}
		switch frame.Type {
		case "message":
			if srv.decodeEventFrame(frame, path) == nil {
				return
			}
		case "complete":
			_ = websocket.Message.Send(ws, `{"result":{"resultId":"sendEvents-1","accepted":true}}`)
			return
		case "ping", "pong", "keepalive":
		default:
			srv.s.violation("W2S %s: unexpected frame type %q", path, frame.Type)
			return
		}
	}
}

func (srv w2sServer) handleChat(ws *websocket.Conn) {
	const path = "/contractclient/ContractApi/AssistEvents"
	srv.s.bump(&srv.s.W2SChatConnections)
	if got := ws.Request().URL.Path; got != path {
		srv.s.violation("W2S chat: unexpected websocket path %s", got)
		return
	}
	if !srv.requireStart(ws, path) {
		return
	}
	for {
		frame, err := srv.recvFrame(ws, path)
		if err != nil {
			// Client closed the socket (handle.close()) — normal chat teardown.
			return
		}
		switch frame.Type {
		case "message":
			if srv.decodeEventFrame(frame, path) == nil {
				return
			}
			if err := websocket.Message.Send(ws, `{"result":{"eventId":"event-echo","tenantNamespace":"tenant.alpha","payload":"reply"}}`); err != nil {
				srv.s.violation("W2S %s: send echo: %v", path, err)
				return
			}
		case "complete":
			return
		case "ping", "pong", "keepalive":
		default:
			srv.s.violation("W2S %s: unexpected frame type %q", path, frame.Type)
			return
		}
	}
}

func main() {
	s := newStats()

	// Main C2S gRPC server: real generated wrappers, auth wired.
	grpcSrv := grpc.NewServer()
	apiWrapper := server.NewContractApi(apiHandler{s: s})
	wireAuth(apiWrapper, s)
	if err := apiWrapper.RegisterServices(grpcSrv); err != nil {
		fatal("register api wrapper: %v", err)
	}
	auditWrapper := auditserver.NewContractAuditApi(auditHandler{s: s})
	auditWrapper.SetEmitter(am.NewEmitter("runtime-smoke-fixture", accountingSink{s: s}))
	auditWrapper.SetTracePublisher(func(_ context.Context, event *trace_pb.TraceEvent) {
		s.recordTrace(event)
	})
	if err := auditWrapper.RegisterServices(grpcSrv); err != nil {
		fatal("register audit wrapper: %v", err)
	}
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("listen grpc: %v", err)
	}
	go func() {
		if err := grpcSrv.Serve(grpcLis); err != nil {
			fatal("serve grpc: %v", err)
		}
	}()

	// Unwired C2S gRPC server: mandatory-auth wrapper with NO auth funcs.
	// Clients assert the fail-closed rejection ("authorizer not configured")
	// over the wire and that the handler is never reached.
	unwiredSrv := grpc.NewServer()
	unwiredWrapper := server.NewContractApi(unwiredHandler{apiHandler: apiHandler{s: s}, s: s})
	if unwiredWrapper.HasAuthorizer() {
		fatal("unwired wrapper unexpectedly reports an authorizer")
	}
	if err := unwiredWrapper.RegisterServices(unwiredSrv); err != nil {
		fatal("register unwired wrapper: %v", err)
	}
	unwiredLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("listen unwired grpc: %v", err)
	}
	go func() {
		if err := unwiredSrv.Serve(unwiredLis); err != nil {
			fatal("serve unwired grpc: %v", err)
		}
	}()

	// W2S HTTP server + fixture stats endpoint.
	w2s := w2sServer{s: s}
	mux := http.NewServeMux()
	mux.HandleFunc("/contractclient/ContractApi/GetPublicContract", w2s.handlePublicRead)
	mux.HandleFunc("/contractclient/ContractApi/GetContract", w2s.handleRead)
	mux.HandleFunc("/contractclient/ContractApi/Watch", w2s.handleWatch)
	mux.HandleFunc("/contractclient/ContractApi/ListEvents", w2s.handleListEvents)
	mux.HandleFunc("/contractclient/ContractApi/ListEventsPaged", w2s.handlePaged)
	mux.Handle("/contractclient/ContractApi/SendEvents", wsHandler(w2s.handleUpload))
	mux.Handle("/contractclient/ContractApi/AssistEvents", wsHandler(w2s.handleChat))
	mux.HandleFunc("/fixture/stats", func(w http.ResponseWriter, r *http.Request) {
		body, err := s.snapshotJSON()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write(body)
	})
	httpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("listen http: %v", err)
	}
	httpServer := &http.Server{Handler: mux}
	go func() {
		if err := httpServer.Serve(httpLis); err != nil && err != http.ErrServerClosed {
			fatal("serve http: %v", err)
		}
	}()

	ports, err := json.Marshal(map[string]string{
		"grpc":        grpcLis.Addr().String(),
		"grpcUnwired": unwiredLis.Addr().String(),
		"http":        "http://" + httpLis.Addr().String(),
	})
	if err != nil {
		fatal("marshal ports: %v", err)
	}
	fmt.Println(string(ports))

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	_ = httpServer.Close()
	grpcSrv.Stop()
	unwiredSrv.Stop()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FIXTURE FATAL: "+format+"\n", args...)
	os.Exit(1)
}
