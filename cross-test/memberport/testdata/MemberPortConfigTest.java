// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package com.example.grid.model.chassis.memberport;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.grid.model.s2sm.ServiceManager;
import com.example.grid.model.types.sys.grpcconfig.Grpcconfig.Config;
import com.example.grid.model.types.sys.grpcconfig.Grpcconfig.MemberPortConfig;
import com.example.grid.model.types.sys.host.HostOuterClass.Host;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.net.InetSocketAddress;
import org.junit.jupiter.api.Test;

/**
 * Black-box traffic stays inside the service's pods (tacplus#258; tacplus-dev/roadmap
 * decisions/s2s-never-crosses-tenants-2026-10-31.md): the generated ServiceManager of a
 * (spec.member_port) service checks grpcconfig.member_port before startup and refuses a
 * target_address that is not loopback, so nothing reaches the black-box protocol except
 * through the member port, which admits only a member of the same service and tenant.
 */
class MemberPortConfigTest {

    private static InetSocketAddress[] addresses(String targetHost) throws Throwable {
        Config cfg = Config.newBuilder()
                .setMemberPort(MemberPortConfig.newBuilder()
                        .setListenAddress(Host.newBuilder().setHost("0.0.0.0").setPort(7000))
                        .setTargetAddress(Host.newBuilder().setHost(targetHost).setPort(5701)))
                .setSpiffeTrustDomain("example.com")
                .addTrustedCas(com.example.grid.model.types.sys.x509.X509.X509Cert.getDefaultInstance())
                .build();
        Method m = ServiceManager.class.getDeclaredMethod("memberPortAddresses", Config.class);
        m.setAccessible(true);
        try {
            return (InetSocketAddress[]) m.invoke(null, cfg);
        } catch (InvocationTargetException e) {
            throw e.getCause();
        }
    }

    @Test
    void nonLoopbackTargetIsRefused() {
        for (String host : new String[] {"0.0.0.0", "10.0.0.5", "192.168.1.20", "::", "fd00::5", "member.invalid"}) {
            IllegalArgumentException e = assertThrows(IllegalArgumentException.class, () -> addresses(host),
                    "target_address " + host + " must be refused");
            assertTrue(e.getMessage().contains("target_address must be a loopback address"), e.getMessage());
        }
    }

    @Test
    void loopbackTargetIsAccepted() throws Throwable {
        for (String host : new String[] {"127.0.0.1", "127.0.0.2", "::1"}) {
            InetSocketAddress[] got = addresses(host);
            assertTrue(got[1].getAddress().isLoopbackAddress(), host);
            assertEquals(5701, got[1].getPort());
        }
    }
}
