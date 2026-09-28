// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
)

// A TLS handshake needs the serving cert chain AND the private key that matches
// it. Both go and java used to resolve those with two independent reads of the
// ServiceManager's cert state:
//
//	go   grpcserver.go.tmpl:   chain := ps.getChain(); ... buildTLSCertificate(chain, ps.getKey())
//	java GrpcServer.java.tmpl: getCertificateChain() -> certProvider.getChain()
//	                           getPrivateKey()       -> certProvider.getPrivateKey()
//
// A ManageCert renewal landing between the two reads pairs chain N with key
// N+1. In go that fails buildTLSCertificate, which logs and reports the
// INSTANCE DEGRADED even though nothing is wrong with either certificate; in
// java the two calls are made by JSSE per handshake, so the peer is handed a
// mismatched chain/key pair and the handshake fails outright. rust never had
// the bug: its resolver hands out an Arc<CertifiedKey>, one object carrying
// both halves.
//
// The fix is the same shape in both languages: one call returns both halves of
// a single snapshot. This test pins that the torn two-call shape does not come
// back.
func TestServingCertChainAndKeyAreResolvedTogether(t *testing.T) {
	goSrv, err := tmplutil.LoadTemplate("go/chassis/grpcserver", "grpcserver.go.tmpl")
	if err != nil {
		t.Fatalf("load grpcserver.go.tmpl: %v", err)
	}
	if !strings.Contains(goSrv, "chain, key := ps.getMaterial()") {
		t.Errorf("go GetCertificate must resolve chain+key from ONE provider call; " +
			`missing "chain, key := ps.getMaterial()"`)
	}
	if !strings.Contains(goSrv, "buildTLSCertificate(chain, key)") {
		t.Errorf(`go must build the renewed cert from the paired snapshot; missing "buildTLSCertificate(chain, key)"`)
	}
	if strings.Contains(goSrv, "ps.getKey()") || strings.Contains(goSrv, "ps.getChain()") {
		t.Errorf("go grpcserver must not read the chain and the key through separate provider " +
			"calls — a renewal between them pairs chain N with key N+1 and spuriously " +
			"reports the instance degraded")
	}

	goLib, err := tmplutil.LoadTemplate("go/types", "servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("load servicemanager_lib.go.tmpl: %v", err)
	}
	if !strings.Contains(goLib, "func (bc *ServiceManager) TLSCertMaterial() (*x509_pb.X509CertChain, gocrypto.PrivateKey) {") {
		t.Errorf("the go ServiceManager must expose a paired chain+key accessor (TLSCertMaterial)")
	}

	goSM, err := tmplutil.LoadTemplate("go/types", "service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("load service_manager.go.tmpl: %v", err)
	}
	if n := strings.Count(goSM, ".SetCertProvider(sm.sc.TLSCertMaterial)"); n == 0 {
		t.Errorf("every go server must be wired to the paired accessor; found no SetCertProvider(sm.sc.TLSCertMaterial) call")
	}
	if strings.Contains(goSM, "SetCertProvider(sm.sc.TLSCertChain, sm.sc.TLSPrivateKey)") {
		t.Errorf("go servers must not be wired to the two separate accessors")
	}

	javaSrv, err := tmplutil.LoadTemplate("java/chassis/grpcserver", "GrpcServer.java.tmpl")
	if err != nil {
		t.Fatalf("load GrpcServer.java.tmpl: %v", err)
	}
	for _, want := range []string{
		"default CertMaterial getMaterial() {",
		"private final ThreadLocal<CertMaterial> handshakeMaterial = new ThreadLocal<>();",
		"CertMaterial material = certProvider.getMaterial();",
		"handshakeMaterial.set(material);",
	} {
		if !strings.Contains(javaSrv, want) {
			t.Errorf("java key manager must pin one chain+key snapshot per handshake; missing %q", want)
		}
	}

	javaSM, err := tmplutil.LoadTemplate("java/types", "ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("load ServiceManager.java.tmpl: %v", err)
	}
	if n := strings.Count(javaSM, "CertAndKey ck = getCertAndKey();"); n < 2 {
		t.Errorf("both java GrpcServer CertProviders must override getMaterial() from the atomic "+
			"CertAndKey snapshot; found %d override(s), want 2", n)
	}
}

// PC-L-1 java parity: go reports a serving certificate it cannot use to the
// fleet health plane (SetCertDegradeReporter). Java only logged it, so an
// instance whose delivered chain will not decode kept advertising itself
// healthy while every TLS handshake failed.
func TestJavaHandshakeCertDecodeFailureReportsDegraded(t *testing.T) {
	javaSrv, err := tmplutil.LoadTemplate("java/chassis/grpcserver", "GrpcServer.java.tmpl")
	if err != nil {
		t.Fatalf("load GrpcServer.java.tmpl: %v", err)
	}
	for _, want := range []string{
		"public void setCertDegradeReporter(java.util.function.Consumer<String> reporter) {",
		`reportCertDegraded("serving TLS certificate could not be decoded for a handshake: " + e.getMessage());`,
	} {
		if !strings.Contains(javaSrv, want) {
			t.Errorf("java GrpcServer must route an undecodable serving cert to the degradation "+
				"seam, not just the log (PC-L-1); missing %q", want)
		}
	}

	javaSM, err := tmplutil.LoadTemplate("java/types", "ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("load ServiceManager.java.tmpl: %v", err)
	}
	servers := strings.Count(javaSM, "new GrpcServer(config, new GrpcServer.CertProvider() {")
	reporters := strings.Count(javaSM, ".setCertDegradeReporter(this::reportTLSDegraded);")
	if servers == 0 || reporters != servers {
		t.Errorf("every java GrpcServer given a live CertProvider must also install the degrade "+
			"reporter (PC-L-1); %d server(s) but %d reporter call(s)", servers, reporters)
	}
}
