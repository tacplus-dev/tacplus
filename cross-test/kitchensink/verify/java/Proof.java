// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// JBMC harnesses over the generated Java validator of the kitchensink member
// (tacplus#196). Each harness states, for every value of the fields it makes
// nondeterministic, that the validator accepts exactly what the proto's
// annotations declare. Not run by cross-test/kitchensink/run.sh yet: JBMC
// cannot model the protobuf-java runtime the validator runs on (#257). To
// try it: copy it into the generated model as proof/java/<package>/Proof.java,
// with a pom.xml there, and run `tacplus verify`.
package com.example.kitchensink.model.types.services.kitchensink;

import com.example.kitchensink.model.types.sys.errors.ProtoException;
import org.cprover.CProver;

public class Proof {

    // Numeric bounds on every integer width, unsigned ones included: a uint32
    // or uint64 is held in a signed Java int or long and must be ordered as
    // unsigned.
    public static void verify_bounds_validate() {
        int port = CProver.nondetInt();
        int gtZero = CProver.nondetInt();
        long geMinusTen = CProver.nondetLong();
        int ltHundred = CProver.nondetInt();
        long leThousand = CProver.nondetLong();
        Kitchensink.Bounds m = Kitchensink.Bounds.newBuilder()
            .setPort(port)
            .setGtZero(gtZero)
            .setGeMinusTen(geMinusTen)
            .setLtHundred(ltHundred)
            .setLeThousand(leThousand)
            .setWindow(1)
            .setOneOf(1)
            .setModeValue(1)
            .build();
        boolean declared = port >= 0 && port <= 65535
            && gtZero > 0
            && geMinusTen >= -10
            && Integer.toUnsignedLong(ltHundred) < 100
            && Long.compareUnsigned(leThousand, 1000) <= 0;
        boolean accepted;
        try {
            Helpers.validateBounds(m);
            accepted = true;
        } catch (ProtoException e) {
            accepted = false;
        }
        assert accepted == declared;
    }
}
