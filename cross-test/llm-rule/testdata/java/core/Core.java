// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
// The core of cross-test/llm-rule's Java service (#909): it builds the
// generated adapter from its ServiceManager and hands the advisor only the port.

package com.gitlab.example.dummy.advised;

import com.gitlab.example.dummy.advised.advisor.Advisor;
import com.gitlab.example.dummy.advised.model.advisor.ModelPort;
import com.gitlab.example.dummy.advised.model.s2sm.ModelPortAdapter;
import com.gitlab.example.dummy.advised.model.s2sm.ServiceManager;

public final class Core {
    private Core() {
    }

    public static ModelPort modelForTheAdvisor(ServiceManager sm) {
        return new ModelPortAdapter(sm);
    }

    public static String answer(ServiceManager sm, String traceId, String question) {
        return new Advisor(modelForTheAdvisor(sm)).ask(traceId, question);
    }
}
