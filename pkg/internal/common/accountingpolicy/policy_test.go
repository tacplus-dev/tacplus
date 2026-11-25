// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package accountingpolicy

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		stage           string
		code            int
		outcome, reason string
	}{
		{StageHandler, 0, OutcomePermitted, ReasonUndefined},
		{StageAuth, 16, OutcomeDeniedAuthn, ReasonTokenRejected},
		{StageAuth, 7, OutcomeDeniedAuthz, ReasonNotEntitled},
		{StageRequest, 7, OutcomeDeniedAuthz, ReasonPolicyDenied},
		{StageRequest, 3, OutcomeInvalid, ReasonRequestInvalid},
		{StageHandler, 5, OutcomeInvalid, ReasonHandlerError},
		{StageHandler, 13, OutcomeFailed, ReasonHandlerError},
		{StageResponse, 13, OutcomeFailed, ReasonResponseInvalid},
		{StageAuth, 14, OutcomeFailed, ReasonInternal},
	}
	for _, c := range cases {
		o, r := Classify(c.stage, c.code)
		if o != c.outcome || r != c.reason {
			t.Errorf("Classify(%s, %d) = %s, %s; want %s, %s", c.stage, c.code, o, r, c.outcome, c.reason)
		}
	}
}

// Every stage has a reason for every outcome other than PERMITTED, exactly once,
// so no rendered classification falls through to a default.
func TestStageReasonsComplete(t *testing.T) {
	seen := map[[2]string]int{}
	for _, r := range StageReasons {
		seen[[2]string{r.Stage, r.Outcome}]++
	}
	for _, s := range Stages {
		for _, o := range []string{OutcomeDeniedAuthn, OutcomeDeniedAuthz, OutcomeInvalid, OutcomeFailed} {
			if seen[[2]string{s, o}] != 1 {
				t.Errorf("stage %s outcome %s: %d reasons, want 1", s, o, seen[[2]string{s, o}])
			}
		}
	}
	for o := range OutcomeLevel {
		if OutcomeLevel[o] == "" {
			t.Errorf("outcome %s has no level", o)
		}
	}
}
