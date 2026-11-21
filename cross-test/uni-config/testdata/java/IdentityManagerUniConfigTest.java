// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.uniconfig.model.chassis.service_im;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.uniconfig.model.types.sys.grpcconfig.Grpcconfig.Config;
import org.junit.jupiter.api.Test;

// The projected Kubernetes token file comes from the config's k8s_token_file.
// Java cannot set environment variables in-process, so that
// KERNEL_K8S_TOKEN_FILE is ignored is held by a template source test.
class IdentityManagerUniConfigTest {
    @Test
    void javaK8sTokenFileComesFromConfig() {
        Config cfg = Config.newBuilder()
                .setK8STokenFile("/var/run/secrets/tacplus/kernel/token")
                .build();
        assertEquals("/var/run/secrets/tacplus/kernel/token",
                IdentityManager.bootstrapSources(cfg).k8sTokenFile());
    }

    @Test
    void javaK8sTokenFileUnsetInConfigIsNotConfigured() {
        String k8s = IdentityManager.bootstrapSources(Config.getDefaultInstance()).k8sTokenFile();
        assertTrue(k8s == null || k8s.isEmpty(), "no k8s_token_file in config must mean none, got " + k8s);
    }
}
