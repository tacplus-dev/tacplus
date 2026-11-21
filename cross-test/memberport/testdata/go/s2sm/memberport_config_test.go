// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package sm

import (
	"net"
	"strings"
	"testing"

	grpcconfig_pb "example.com/grid/tacplus/model/types/sys/grpcconfig"
	host_pb "example.com/grid/tacplus/model/types/sys/host"
	x509_pb "example.com/grid/tacplus/model/types/sys/x509"
)

// Black-box traffic stays inside the service's pods (tacplus#258; tacplus-dev/roadmap
// decisions/s2s-never-crosses-tenants-2026-10-31.md): the generated ServiceManager of a
// (spec.member_port) service checks grpcconfig.member_port before startup and refuses a
// target_address that is not loopback, so nothing reaches the black-box protocol except
// through the member port. The Go twin of MemberPortConfigTest.java.

func memberPortConfig(targetHost string) *grpcconfig_pb.Config {
	return &grpcconfig_pb.Config{
		MemberPort: &grpcconfig_pb.MemberPortConfig{
			ListenAddress: &host_pb.Host{Host: "0.0.0.0", Port: 7000},
			TargetAddress: &host_pb.Host{Host: targetHost, Port: 5701},
		},
		SpiffeTrustDomain: "example.com",
		TrustedCas:        []*x509_pb.X509Cert{{}},
	}
}

func TestMemberPortConfigNonLoopbackTargetIsRefused(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "10.0.0.5", "192.168.1.20", "::", "fd00::5", "member.invalid"} {
		_, _, err := memberPortAddresses(memberPortConfig(host))
		if err == nil {
			t.Fatalf("target_address %s must be refused", host)
		}
		if !strings.Contains(err.Error(), "target_address must be a loopback address") {
			t.Fatalf("%s: refused for another reason: %v", host, err)
		}
	}
}

func TestMemberPortConfigLoopbackTargetIsAccepted(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.0.0.2", "::1"} {
		_, target, err := memberPortAddresses(memberPortConfig(host))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		h, port, err := net.SplitHostPort(target)
		if err != nil || !net.ParseIP(h).IsLoopback() || port != "5701" {
			t.Fatalf("%s: target %q", host, target)
		}
	}
}
