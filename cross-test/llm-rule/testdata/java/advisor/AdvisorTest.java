// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.gitlab.example.dummy.advised.advisor;

import static org.junit.jupiter.api.Assertions.assertEquals;

import com.gitlab.example.dummy.advised.model.advisor.ModelPort;
import com.gitlab.example.dummy.advised.model.types.services.upstreamllm.Upstreamllm;
import org.junit.jupiter.api.Test;

class AdvisorTest {
    @Test
    void theAdvisorAsksThroughThePortOnItsTrace() {
        ModelPort echo = (traceId, req) -> Upstreamllm.CompleteResponse.newBuilder().setText(traceId + ":" + req.getPrompt()).build();
        assertEquals("trace-1:ping", new Advisor(echo).ask("trace-1", "ping"));
    }
}
