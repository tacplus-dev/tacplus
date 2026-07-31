package node

import (
	"testing"
	"time"
)

func TestVerifyFailsClosed(t *testing.T) {
	id := NewIdentity(IdentityConfig{})

	good, err := id.Issue("ns", "sub", true, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := id.Verify(good, "ns", true); err != nil {
		t.Fatalf("valid token should verify: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(Token) Token
		reqNS  string
		reqEnt bool
	}{
		{"wrong-namespace", func(tok Token) Token { return tok }, "other", true},
		{"tampered", func(tok Token) Token { tok.Subject = "evil"; return tok }, "ns", true},
		{"missing-signature", func(tok Token) Token { tok.signature = ""; return tok }, "ns", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := id.Verify(tc.mutate(good), tc.reqNS, tc.reqEnt); err == nil {
				t.Fatalf("%s should be denied (fail-closed)", tc.name)
			}
		})
	}
}

func TestVerifyExpiredFailsClosedWhileIssuerDown(t *testing.T) {
	id := NewIdentity(IdentityConfig{})
	tok, _ := id.Issue("ns", "sub", true, -time.Hour) // already expired
	id.SetRefreshFails(true)                          // issuer down

	if err := id.Verify(tok, "ns", true); err == nil {
		t.Fatal("expired token must be denied even while issuer is down (no fail-open)")
	}
	if _, err := id.Refresh(tok, time.Hour); err == nil {
		t.Fatal("refresh must error while issuer is down")
	}
}
