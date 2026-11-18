// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-record fixture's WRITER build (#284). NOT
// generated: run.sh copies this file into the generated s2sm package before
// `go test ./...`, so it drives the REAL generated client-facing record reader
// ((spec.record_read).c2s, #207) of tmessaging, whose plane is routed by tenant.
//
// What it pins: the reader goes to the caller's own tenant, its token's tid
// claim, and never to a tid read off its namespace claim, which names the
// tenant (/tenants/<name>, kernel#23). Tenant beta is tid bbbbbbbb; tenant
// alpha is tid aaaaaaaa. A caller of beta is read from bbbbbbbb, including one
// of a tenant NAMED aaaaaaaa (a name shaped like alpha's tid), which never
// reaches alpha. A caller with no tid claim is refused, whatever its namespace
// spells, before any instance is asked.

package sm

import (
	"context"
	"testing"

	"gitlab.com/example/dummy/tmessaging/tacplus/model/chassis/servicemanager"
	wrapperToken "gitlab.com/example/dummy/tmessaging/tacplus/model/chassis/token"
	tmessaging_pb "gitlab.com/example/dummy/tmessaging/tacplus/model/types/services/tmessaging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func c2sCaller(ns, tid string) *servicemanager.Caller {
	tok := wrapperToken.NewImmutableToken(nil).
		SetSubType("user").
		SetSubject("spiffe://example.com/tenants/beta/users/alice").
		SetNamespace(ns)
	if tid != "" {
		tok = tok.SetTid(tid)
	}
	return servicemanager.NewCaller(tok, "c2s-test")
}

// A caller is read from the tenant of its tid claim, whatever its namespace
// names.
func TestTenantRecordC2SReadsTheCallersTidClaim(t *testing.T) {
	for _, tc := range []struct{ ns, tid string }{
		{"/tenants/beta", "bbbbbbbb"},
		{"/tenants/beta/workspaces/dev", "bbbbbbbb"},
		// Tenant beta named like alpha's tid: still beta's instance.
		{"/tenants/aaaaaaaa", "bbbbbbbb"},
	} {
		got, err := recordC2STenant("GetTMessage", c2sCaller(tc.ns, tc.tid))
		if err != nil || got != "bbbbbbbb" {
			t.Fatalf("caller in %s with tid %s is read from tenant %q (err %v), want bbbbbbbb", tc.ns, tc.tid, got, err)
		}
	}
}

// No tid claim, or a malformed one, is no tenant: the namespace is never read
// for one. The handler refuses before it reaches the store (its manager is nil
// here, so a read that got past the refusal would panic).
func TestTenantRecordC2SRefusesACallerWithoutATid(t *testing.T) {
	h := &TmessagingRecordsGeneratedHandler{}
	for _, tc := range []struct{ ns, tid string }{
		{"/tenants/bbbbbbbb", ""},
		{"/tenants/aaaaaaaa/workspaces/dev", ""},
		{"/system", ""},
		{"/tenants/beta", "BBBBBBBB"},
		{"/tenants/beta", "/tenants/bbbbbbbb"},
	} {
		caller := c2sCaller(tc.ns, tc.tid)
		if _, err := recordC2STenant("GetTMessage", caller); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("caller in %s with tid %q was given a tenant: %v", tc.ns, tc.tid, err)
		}
		if _, err := h.GetTMessage(context.Background(), caller, &tmessaging_pb.GetTMessageRequest{MessageId: "m1"}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("GetTMessage for caller in %s with tid %q: %v, want PERMISSION_DENIED", tc.ns, tc.tid, err)
		}
		if _, err := h.GetTMessagePage(context.Background(), caller, &tmessaging_pb.GetTMessagePageRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("GetTMessagePage for caller in %s with tid %q: %v, want PERMISSION_DENIED", tc.ns, tc.tid, err)
		}
	}
}
