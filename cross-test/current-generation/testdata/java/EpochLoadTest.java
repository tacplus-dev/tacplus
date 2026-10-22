// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the epoch-load fixture. NOT generated: run.sh copies this
// file into the generated chassis/diskstore/stores package beside DiskStores.java
// and runs it as a main class.
//
// What it pins: Lease carries an Expiry whose exp is (spec.epoch_expiration), a
// write-time assertion (a future epoch). A record written while valid must still
// LOAD after its exp has passed, or a service cannot restart once any stored
// record ages out. The paired control: a non-time violation (an empty lease_id)
// must still fail the load, even when the record is also expired. This executes
// the generated DiskStores.loadLease, which is the path the disk loader runs.

package com.gitlab.example.dummy.epochload.model.chassis.diskstore.stores;

import java.nio.file.Files;
import java.nio.file.Path;

import com.gitlab.example.dummy.epochload.model.chassis.diskstore.Snapshot;
import com.gitlab.example.dummy.epochload.model.types.services.epochload.Epochload.Expiry;
import com.gitlab.example.dummy.epochload.model.types.services.epochload.Epochload.Lease;

public final class EpochLoadTest {
    private EpochLoadTest() {}

    private static long nowSeconds() {
        return System.currentTimeMillis() / 1000L;
    }

    // saveThenAge writes one Lease, then waits until its exp has passed.
    private static void saveThenAge(Path base, Lease lease) throws Exception {
        DiskStores.saveLease(base, "epochload", lease, Snapshot.Opts.defaults());
        long exp = lease.getExpiry().getExp();
        while (nowSeconds() <= exp) {
            Thread.sleep(100L);
        }
    }

    private static void checkAcceptsAnEpochThatExpiredAfterItWasWritten() throws Exception {
        Path base = Files.createTempDirectory("epoch-load-accepts");
        long exp = nowSeconds() + 1L;
        Lease lease = Lease.newBuilder()
                .setLeaseId("lease-1")
                .setExpiry(Expiry.newBuilder().setExp(exp).build())
                .build();
        saveThenAge(base, lease);

        Lease got;
        try {
            got = DiskStores.loadLease(base, "epochload");
        } catch (RuntimeException e) {
            throw new AssertionError("loadLease after exp " + exp + " has passed must succeed (data ages; the owner retires it)", e);
        }
        if (got == null) {
            throw new AssertionError("loadLease: snapshot not found");
        }
        if (!got.getLeaseId().equals("lease-1") || got.getExpiry().getExp() != exp) {
            throw new AssertionError("loadLease round-trip mismatch: lease_id=" + got.getLeaseId()
                    + " exp=" + got.getExpiry().getExp() + ", want lease-1 exp=" + exp);
        }
    }

    private static void checkStillRejectsANonTimeViolation() throws Exception {
        Path base = Files.createTempDirectory("epoch-load-rejects");
        long exp = nowSeconds() + 1L;
        Lease lease = Lease.newBuilder()
                .setLeaseId("")
                .setExpiry(Expiry.newBuilder().setExp(exp).build())
                .build();
        saveThenAge(base, lease);

        boolean rejected = false;
        try {
            DiskStores.loadLease(base, "epochload");
        } catch (RuntimeException expected) {
            rejected = true;
        }
        if (!rejected) {
            throw new AssertionError("loadLease with an empty lease_id must fail even when exp has passed");
        }
    }

    public static void main(String[] args) throws Exception {
        checkAcceptsAnEpochThatExpiredAfterItWasWritten();
        checkStillRejectsANonTimeViolation();
        System.out.println("EpochLoadTest: all checks passed");
    }
}
