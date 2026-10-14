// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// server-runtime driver: the GENERATED GO C2S CLIENT, pointed at a generated
// JAVA or RUST server hosted by this gate's fixtures. Every assertion below is
// about what the remote generated server wrapper did — the go side only dials,
// calls, and checks the bytes that came back.
//
// Reads TACPLUS_SERVER_ADDR (host:port) and TACPLUS_SERVER_LANG (label used in
// failure messages only).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	c2sm "gitlab.com/example/tacplus/consumer/contractclient/model/c2sm"
	client "gitlab.com/example/tacplus/consumer/contractclient/model/c2sm/grpc/clients/contract/contractclient"
	types "gitlab.com/example/tacplus/consumer/contractclient/model/types/contract/contractclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const fixtureToken = "fixture-token"

func validRequest() *types.ContractRequest {
	return &types.ContractRequest{
		TenantNamespace: "tenant.alpha",
		ItemId:          "item-123",
		Mode:            types.ContractMode_CONTRACT_MODE_READ,
		Tags:            []string{"one"},
		Metadata:        map[string]string{"owner": "server-runtime"},
	}
}

func main() {
	addr := os.Getenv("TACPLUS_SERVER_ADDR")
	if addr == "" {
		fail("TACPLUS_SERVER_ADDR is required")
	}
	lang := os.Getenv("TACPLUS_SERVER_LANG")
	if lang == "" {
		fail("TACPLUS_SERVER_LANG is required")
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fail("dial %s server at %s: %v", lang, addr, err)
	}
	defer conn.Close()

	sm := c2sm.New(conn, func() (string, error) { return fixtureToken, nil })
	api := sm.Grpc().Clients().Contract().ContractApiClient()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	checkUnary(ctx, api, lang)
	checkServerStream(ctx, api, lang)
	checkPage(ctx, api, lang)
	checkFailClosed(ctx, api, lang)

	fmt.Printf("go generated client OK against %s generated server (%s)\n", lang, addr)
}

// checkUnary: auth_disabled unary. The generated remote wrapper validates the
// request, dispatches to the handler, validates the response, and marshals it
// back. Exact field values prove real generated code round-tripped.
func checkUnary(ctx context.Context, api *client.ContractApiClient, lang string) {
	resp, err := api.GetPublicContract(ctx, validRequest())
	if err != nil {
		fail("%s GetPublicContract: %v", lang, err)
	}
	if resp.GetResultId() != "result-123" {
		fail("%s GetPublicContract result_id = %q, want %q", lang, resp.GetResultId(), "result-123")
	}
	if !resp.GetAccepted() {
		fail("%s GetPublicContract accepted = false, want true", lang)
	}
	fmt.Printf("  %s: GetPublicContract unary round-trip ok\n", lang)
}

// checkServerStream: auth_any_user server-streaming (SNAPSHOT collection mode).
func checkServerStream(ctx context.Context, api *client.ContractApiClient, lang string) {
	seq, err := api.ListEvents(ctx, validRequest())
	if err != nil {
		fail("%s ListEvents: %v", lang, err)
	}
	var ids []string
	for event, err := range seq {
		if err != nil {
			fail("%s ListEvents stream: %v", lang, err)
		}
		if event.GetTenantNamespace() != "tenant.alpha" {
			fail("%s ListEvents tenant_namespace = %q, want %q", lang, event.GetTenantNamespace(), "tenant.alpha")
		}
		ids = append(ids, event.GetEventId())
	}
	if len(ids) != 1 || ids[0] != "event-1" {
		fail("%s ListEvents event ids = %v, want [event-1]", lang, ids)
	}
	fmt.Printf("  %s: ListEvents SNAPSHOT stream round-trip ok\n", lang)
}

// checkPage: auth_any_user PAGE. The generated client auto-pager follows the
// remote server's next_page_token, so two pages here prove the remote server
// encoded a resume cursor and decoded it back on the follow-up call.
func checkPage(ctx context.Context, api *client.ContractApiClient, lang string) {
	var ids []string
	for event, err := range api.ListEventsPaged(ctx, &types.ContractPageRequest{
		TenantNamespace: "tenant.alpha",
		PageSize:        10,
	}) {
		if err != nil {
			fail("%s ListEventsPaged: %v", lang, err)
		}
		ids = append(ids, event.GetEventId())
	}
	if len(ids) != 2 || ids[0] != "event-1" || ids[1] != "event-2" {
		fail("%s ListEventsPaged event ids = %v, want [event-1 event-2]", lang, ids)
	}
	fmt.Printf("  %s: ListEventsPaged PAGE keyset continuation round-trip ok\n", lang)
}

// checkFailClosed: auth_entitled_users unary with a token the remote server
// cannot accept. The remote generated wrapper must reject before the handler —
// the fixture handlers return result_id "HANDLER-REACHED" for this method, so a
// success (or that marker) here means auth was bypassed.
//
// Both hosted fixtures run the REAL generated Authorizer over an explicitly
// empty verifying-key set, so the opaque fixture bearer is rejected at token
// verification in both languages: UNAUTHENTICATED, exactly, either side.
func checkFailClosed(ctx context.Context, api *client.ContractApiClient, lang string) {
	resp, err := api.GetContract(ctx, validRequest())
	if err == nil {
		fail("%s GetContract was accepted (result_id=%q) — auth is not fail-closed", lang, resp.GetResultId())
	}
	code := status.Code(err)
	if code != codes.Unauthenticated {
		fail("%s GetContract rejected with %s, want Unauthenticated: %v", lang, code, err)
	}
	fmt.Printf("  %s: GetContract fail-closed rejection ok (%s)\n", lang, code)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "server-runtime client FAILED: "+format+"\n", args...)
	os.Exit(1)
}
