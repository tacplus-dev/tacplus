// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
// The green Java advisor of cross-test/llm-rule (#909): it reaches its model
// only through the generated model port, on the trace it is answering.

package com.gitlab.example.dummy.advised.advisor;

import com.gitlab.example.dummy.advised.model.advisor.ModelPort;
import com.gitlab.example.dummy.advised.model.advisor.ModelPortException;
import com.gitlab.example.dummy.advised.model.types.services.upstreamllm.Upstreamllm;

public final class Advisor {
    private final ModelPort model;

    public Advisor(ModelPort model) {
        this.model = model;
    }

    public String ask(String traceId, String prompt) {
        try {
            return model.generate(traceId, Upstreamllm.CompleteRequest.newBuilder().setPrompt(prompt).build()).getText();
        } catch (ModelPortException e) {
            return "error " + e.getCode();
        }
    }
}
