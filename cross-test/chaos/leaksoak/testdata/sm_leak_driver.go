// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Leak-soak driver: copied into a freshly generated Go model and run there, so
// the resource counters it prints come from a REAL generated ServiceManager
// rather than a synthetic stand-in (docs/KNOWN_TEST_GAPS.md PC-L-9).
//
// One cycle = servicemanager.NewServiceManager(...) + Close(). That is the
// whole teardown surface: New() dials every configured endpoint, pings it,
// and starts the ping / rebalance / Log / Subscribe / WatchJwks loops; Close()
// cancels their contexts and closes the pool. Nothing else is needed to make
// the goroutine fleet appear — the peak scrape proves it did.
//
// Prints one JSON object on stdout:
//
//	{"baseline":{...},"peak":{...},"cycles":[{...},...],"fds_supported":bool}
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"time"

	"example.com/certstream/tacplus/model/chassis/servicemanager"
	grpcconfig_pb "example.com/certstream/tacplus/model/types/sys/grpcconfig"
	host_pb "example.com/certstream/tacplus/model/types/sys/host"
	x509_pb "example.com/certstream/tacplus/model/types/sys/x509"
)

// cycles is the number of measured create/teardown cycles. A leak shows only
// as a trend, so one cycle proves nothing; this is the soak length.
const cycles = 8

type scrape struct {
	Goroutines int64 `json:"goroutines"`
	OpenFDs    int64 `json:"open_fds"`
}

type result struct {
	Baseline     scrape   `json:"baseline"`
	Peak         scrape   `json:"peak"`
	Cycles       []scrape `json:"cycles"`
	FDsSupported bool     `json:"fds_supported"`
}

func main() {
	ca := selfSignedCA()

	// Warm-up cycle before the baseline: the first construction pulls in
	// process-wide singletons (zap globals, grpc/net resolver state) that are
	// never freed and are not a leak. Baselining after it measures teardown,
	// not first-use initialisation.
	if _, err := runCycle(ca); err != nil {
		fmt.Fprintf(os.Stderr, "warm-up cycle: %v\n", err)
		os.Exit(1)
	}
	settle()

	res := result{Baseline: scrapeNow(), FDsSupported: fdsSupported()}
	for i := 0; i < cycles; i++ {
		peak, err := runCycle(ca)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cycle %d: %v\n", i, err)
			os.Exit(1)
		}
		if peak.Goroutines > res.Peak.Goroutines {
			res.Peak = peak
		}
		settle()
		res.Cycles = append(res.Cycles, scrapeNow())
	}

	enc, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal result: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(enc))
}

// runCycle constructs a generated ServiceManager, scrapes it while it is live,
// and tears it down. The live scrape is what proves the cycle exercised
// something: a ServiceManager that started no goroutines could not leak one.
func runCycle(ca *x509_pb.X509Cert) (scrape, error) {
	cfg := &grpcconfig_pb.Config{
		// Port 1 is never listening. The endpoint is dialled and pinged for
		// real; an unreachable one keeps every stream loop in its reconnect
		// path, which is the teardown surface being measured.
		Endpoints:         []*host_pb.Host{{Host: "127.0.0.1", Port: 1}},
		TrustedCas:        []*x509_pb.X509Cert{ca},
		SpiffeTrustDomain: "leak-soak.test",
	}
	// identity == nil: unauthenticated pool, so no kernel/token authority is
	// needed. Everything else in New() runs exactly as in production.
	sm, err := servicemanager.NewServiceManager(context.Background(), cfg, nil)
	if err != nil {
		return scrape{}, fmt.Errorf("NewServiceManager: %w", err)
	}
	live := scrapeNow()
	if err := sm.Close(); err != nil {
		return live, fmt.Errorf("Close: %w", err)
	}
	return live, nil
}

func scrapeNow() scrape {
	fds, _ := openFDs()
	return scrape{Goroutines: int64(runtime.NumGoroutine()), OpenFDs: fds}
}

// settle waits for the post-Close goroutine count to stop moving. Close()
// cancels contexts without joining, so the scrape has to wait for the loops to
// observe the cancellation. Polling for a stable count beats a fixed sleep,
// which is either flaky or slow.
func settle() {
	const stableSamples = 5
	prev := -1
	stable := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		n := runtime.NumGoroutine()
		if n == prev {
			if stable++; stable >= stableSamples {
				return
			}
		} else {
			stable = 0
			prev = n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// openFDs counts this process's open file descriptors. /proc/self/fd is the
// only mechanism available without shelling out or cgo, so the FD leg of the
// leak oracle is exercised on Linux and reported as unsupported elsewhere
// (macOS has no /proc). The goroutine leg runs everywhere.
func openFDs() (int64, bool) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	// The ReadDir handle itself is one of the entries; it is open for every
	// scrape alike, so the trend is unaffected.
	return int64(len(entries)), true
}

func fdsSupported() bool {
	_, ok := openFDs()
	return ok
}

// selfSignedCA builds the trusted-CA entry NewServiceManager requires for
// SPIFFE TLS verification. Nothing ever completes a handshake against it (the
// endpoint is dead), but the pool refuses to build without one.
func selfSignedCA() *x509_pb.X509Cert {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "leak-soak-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &x509_pb.X509Cert{
		Id:     "leak-soak-ca",
		Base64: base64.RawURLEncoding.EncodeToString(pemBytes),
	}
}
