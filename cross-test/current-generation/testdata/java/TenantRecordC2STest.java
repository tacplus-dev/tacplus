// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the tenant-routed client-facing record reader (#284). NOT
// generated: run.sh copies this file into the generated s2sm package of the
// tmessaging (record WRITER) build of the tenant-record fixture, beside the
// generated TmessagingRecordsGeneratedHandler, and runs it as a main-method
// self-test, like TenantRecordRouteTest. It lives in that package for the
// handler's package-private requireTenant.
//
// It drives the REAL generated tenant resolution of the c2s reader with Callers
// built from verified-token payloads, exactly as Auth.callerFromContext builds
// them: the tenant a read goes to is the caller's tid claim, which kernel
// stamps on every token whose namespace is in a tenant, and NEVER a tid read
// off the caller's namespace, which names the tenant (/tenants/<name>,
// kernel#23). A caller whose token carries no tid claim, or a malformed one,
// is refused PERMISSION_DENIED, however tid-shaped its namespace's name.
//
// Tenant beta is tid bbbbbbbb, tenant alpha is tid aaaaaaaa.

package com.gitlab.example.dummy.tmessaging.model.s2sm;

import com.gitlab.example.dummy.tmessaging.model.chassis.servicemanager.Caller;
import com.gitlab.example.dummy.tmessaging.model.types.services.tmessaging.Tmessaging;
import com.gitlab.example.dummy.tmessaging.model.types.sys.errors.ProtoException;

public class TenantRecordC2STest {
    private static int failures = 0;

    private static void fail(String message) {
        System.err.println("FAIL: " + message);
        failures++;
    }

    private static final String A = "aaaaaaaa";
    private static final String B = "bbbbbbbb";
    private static final String SUBJECT = "spiffe://example.com/users/u1";

    /** A Caller built from a verified-token payload; tid null means the token carries no tid claim. */
    private static Caller caller(String namespace, String tid) {
        StringBuilder p = new StringBuilder("{\"sub\":\"").append(SUBJECT).append("\",\"namespace\":\"").append(namespace).append('"');
        if (tid != null) {
            p.append(",\"tid\":\"").append(tid).append('"');
        }
        return new Caller(p.append('}').toString(), "c2s-test-request");
    }

    private interface Call {
        Object run() throws Exception;
    }

    private static void wantTenant(String what, Caller c, String want) {
        try {
            String got = TmessagingRecordsGeneratedHandler.requireTenant("GetTMessage", c);
            if (!want.equals(got)) {
                fail(what + ": read goes to tenant " + got + ", want " + want);
            }
        } catch (Exception e) {
            fail(what + ": refused, want tenant " + want + ": " + e);
        }
    }

    private static void wantDenied(String what, Call call) {
        try {
            Object got = call.run();
            fail(what + ": admitted (" + got + "), want PERMISSION_DENIED");
        } catch (ProtoException e) {
            if (e.getCode() != ProtoException.PERMISSION_DENIED) {
                fail(what + ": refused with code " + e.getCode() + ", want PERMISSION_DENIED: " + e.getMessage());
            }
        } catch (Exception e) {
            fail(what + ": refused with an untyped error, want PERMISSION_DENIED: " + e);
        }
    }

    private static void wantRefused(String what, Call call) {
        try {
            Object got = call.run();
            fail(what + ": admitted (" + got + "), want refused");
        } catch (ProtoException e) {
            // refused, by a typed error
        } catch (Exception e) {
            fail(what + ": refused with an untyped error: " + e);
        }
    }

    private static String tenant(Caller c) throws Exception {
        return TmessagingRecordsGeneratedHandler.requireTenant("GetTMessage", c);
    }

    public static void main(String[] args) throws Exception {
        // The tenant is the tid claim; the namespace names the tenant.
        wantTenant("a caller in /tenants/beta with tid bbbbbbbb", caller("/tenants/beta", B), B);
        wantTenant("a caller below /tenants/beta with tid bbbbbbbb", caller("/tenants/beta/workspaces/dev", B), B);

        // A namespace whose name looks like alpha's tid: still beta's read, never alpha's.
        Caller lookalike = caller("/tenants/" + A, B);
        wantTenant("a caller in /tenants/aaaaaaaa with tid bbbbbbbb", lookalike, B);
        if (A.equals(tenant(lookalike))) {
            fail("a caller in /tenants/aaaaaaaa with tid bbbbbbbb reads tenant alpha's store");
        }

        // No tid claim is no tenant: nothing is derived from the namespace.
        wantDenied("a caller in /tenants/bbbbbbbb with no tid claim", () -> tenant(caller("/tenants/" + B, null)));
        wantDenied("a caller in /tenants/beta with no tid claim", () -> tenant(caller("/tenants/beta", null)));
        wantDenied("a /system caller", () -> tenant(caller("/system", null)));
        wantRefused("no caller", () -> tenant(null));

        // A malformed tid claim is no tenant either.
        for (String bad : new String[] {"", "BBBBBBBB", "beta", "bbbbbbbbb", "/tenants/bbbbbbbb", " bbbbbbbb"}) {
            wantDenied("a caller in /tenants/beta with tid claim \"" + bad + "\"", () -> tenant(caller("/tenants/beta", bad)));
        }

        // The generated rpcs refuse such a caller before the store is asked: the
        // handler is given no ServiceManager, so any call that got past the gate
        // would fail untyped, not PERMISSION_DENIED.
        TmessagingRecordsGeneratedHandler h = new TmessagingRecordsGeneratedHandler(null);
        Caller noTid = caller("/tenants/" + B, null);
        wantDenied("GetTMessage from a caller with no tid claim",
            () -> h.getTMessage(noTid, Tmessaging.GetTMessageRequest.getDefaultInstance()));
        wantDenied("GetTMessagePage from a caller with no tid claim",
            () -> h.getTMessagePage(noTid, Tmessaging.GetTMessagePageRequest.getDefaultInstance()));
        wantDenied("GetTMessage from a /system caller",
            () -> h.getTMessage(caller("/system", null), Tmessaging.GetTMessageRequest.getDefaultInstance()));

        if (failures > 0) {
            System.err.println("TenantRecordC2STest: " + failures + " failure(s)");
            System.exit(1);
        }
        System.out.println("TenantRecordC2STest: all checks passed");
    }
}
