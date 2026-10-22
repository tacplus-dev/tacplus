// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the main fixture's extraction helpers. NOT generated:
// run.sh copies this file into the generated common2/user types package before
// the Java compile and runs it as a main-method self-test.
//
// What it pins: common2.FixtureProbe declares the (spec.transformation)
// FixtureProbeRow, so the generator emits FixtureProbeProjections with
// extractFixtureProbeRowFromFixtureProbe and fixtureProbeToProjections — Java's
// port of Go's ExtractFixtureProbeRowFromFixtureProbe / FixtureProbeToProjections
// and Rust's extract_fixture_probe_row_from_fixture_probe /
// fixture_probe_to_projections. A populated event must come out with every
// projection_source field copied; a null event must give null, as Go's nil.

package com.gitlab.example.dummy.proto.model.types.common2.user;

public class ExtractionTest {
    private static int failures = 0;

    private static void check(boolean ok, String what) {
        if (!ok) {
            failures++;
            System.err.println("FAIL: " + what);
        }
    }

    public static void main(String[] args) {
        User.FixtureProbe event = User.FixtureProbe.newBuilder()
            .setProbeId("probe-1")
            .setObservedAtMs(1234L)
            .setKind("disk")
            .build();

        User.FixtureProbeRow row = FixtureProbeProjections.extractFixtureProbeRowFromFixtureProbe(event);
        check(row != null, "extract returned null for a populated event");
        if (row != null) {
            check(row.getProbeId().equals("probe-1"), "probe_id not copied: " + row.getProbeId());
            check(row.getObservedAtMs() == 1234L, "observed_at_ms not copied: " + row.getObservedAtMs());
            check(row.getKind().equals("disk"), "kind not copied: " + row.getKind());
        }

        FixtureProbeProjections all = FixtureProbeProjections.fixtureProbeToProjections(event);
        check(all != null && row != null && row.equals(all.fixtureProbeRow),
            "fixtureProbeToProjections does not carry the extracted row");

        check(FixtureProbeProjections.extractFixtureProbeRowFromFixtureProbe(null) == null,
            "extract of a null event must be null");
        check(FixtureProbeProjections.fixtureProbeToProjections(null) == null,
            "toProjections of a null event must be null");

        if (failures > 0) {
            System.err.println("ExtractionTest: " + failures + " check(s) failed");
            System.exit(1);
        }
        System.out.println("ExtractionTest: all checks passed");
    }
}
