// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
)

// The TLS and SMB certificate streams are the same shape — a bidi stream to an
// authority, authenticated with the service token, retried with backoff — and
// both must drive the credential-degradation seam:
//
//   - note_if_credential_rejection on a stream error, so an UNAUTHENTICATED
//     stream drops the cached token and the next attempt RE-MINTS. Without it
//     the loop retries forever presenting the same stale token and never
//     recovers.
//   - note_credential_accepted on a delivery, so the rejection counter can be
//     cleared. A service with no state watches — the other clear site — would
//     otherwise stay degraded forever after a brief authority blip.
//
// Both call sites were wired into the TLS loops in all three full-service
// languages and into NEITHER of the SMB loops, so an SMB-only service (SMB
// without auto-TLS and without state watches) had no site at all. This test
// holds the two streams to the same contract in every language.
func TestCertStreamLoopsDriveTheCredentialSeam(t *testing.T) {
	for _, lang := range []struct {
		name           string
		dir            string
		file           string
		acceptedCall   string
		rejectionCall  string
		tlsStart       string
		smbStart       string
		streamEndAfter string
	}{
		{
			name:           "go",
			dir:            "go/types",
			file:           "service_manager.go.tmpl",
			acceptedCall:   "service_im.NoteCredentialAccepted()",
			rejectionCall:  "service_im.NoteIfCredentialRejection(err)",
			tlsStart:       "func tlsCertStreamLoop(",
			smbStart:       "func smbCertStreamLoop(",
			streamEndAfter: "cert renewal requested",
		},
		{
			name:           "rust",
			dir:            "rust/types",
			file:           "service_manager.rs.tmpl",
			acceptedCall:   "service_im::note_credential_accepted()",
			rejectionCall:  "service_im::note_if_credential_rejection(reason)",
			tlsStart:       "async fn tls_cert_stream_loop(",
			smbStart:       "async fn smb_cert_stream_loop(",
			streamEndAfter: "cert renewal requested",
		},
		{
			name:           "java",
			dir:            "java/types",
			file:           "ServiceManager.java.tmpl",
			acceptedCall:   "IdentityManager.noteCredentialAccepted()",
			rejectionCall:  "IdentityManager.noteIfCredentialRejection(",
			tlsStart:       "private static void tlsCertStreamLoop(",
			smbStart:       "private static void smbCertStreamLoop(",
			streamEndAfter: "cert renewal requested",
		},
	} {
		t.Run(lang.name, func(t *testing.T) {
			s, err := tmplutil.LoadTemplate(lang.dir, lang.file)
			if err != nil {
				t.Fatalf("load %s: %v", lang.file, err)
			}
			for _, stream := range []struct {
				label string
				start string
			}{
				{"TLS", lang.tlsStart},
				{"SMB", lang.smbStart},
			} {
				region := streamRegion(t, s, stream.start, lang.streamEndAfter)
				if !strings.Contains(region, lang.rejectionCall) {
					t.Errorf("%s %s cert-stream loop never calls %s: an UNAUTHENTICATED stream will retry forever against the same cached token instead of re-minting",
						lang.name, stream.label, lang.rejectionCall)
				}
				if !strings.Contains(region, lang.acceptedCall) {
					t.Errorf("%s %s cert-stream loop never calls %s: nothing can clear the rejection counter for a service whose only credential-driven surface is this stream",
						lang.name, stream.label, lang.acceptedCall)
				}
			}
		})
	}
}

// streamRegion returns the slice of the template starting at start and ending
// at the first occurrence of endAfter past it — the code of one cert-stream
// loop plus its one-session helper.
func streamRegion(t *testing.T, s, start, endAfter string) string {
	t.Helper()
	i := strings.Index(s, start)
	if i < 0 {
		t.Fatalf("marker %q not found — the template was restructured and this test no longer looks at the cert-stream code", start)
	}
	rest := s[i:]
	j := strings.Index(rest, endAfter)
	if j < 0 {
		t.Fatalf("end marker %q not found after %q", endAfter, start)
	}
	return rest[:j]
}
