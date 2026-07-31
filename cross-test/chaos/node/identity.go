package node

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Identity models the generated identity rivet: a minimal test token signer and
// served JWK set, standing in for (never importing) a consumer identity service.
// It exercises the generated verify/refresh/authorizer paths for real.
//
// Tokens are deliberately not real JWTs — the harness asserts the *contract*
// (fail-closed verification, per-message freshness, refresh-failure semantics),
// not a particular crypto library. The signer is an HMAC over the claims so
// verification is deterministic and tamper-evident.
type Identity struct {
	mu sync.RWMutex

	signingKey   []byte
	refreshFails bool          // when set, the issuer is "down": refresh returns an error
	clockSkew    time.Duration // simulated skew applied to issued/validated times
	now          func() time.Time
}

// Token is a signed bearer credential. A real deployment would carry a JWT; here
// the claims plus an HMAC are enough to test the fail-closed verification path.
type Token struct {
	Namespace string
	Entitled  bool
	Subject   string
	NotAfter  time.Time
	signature string
}

// IdentityConfig parameterizes the test signer.
type IdentityConfig struct {
	SigningKey []byte
	TokenTTL   time.Duration
}

// NewIdentity builds a test identity rivet.
func NewIdentity(cfg IdentityConfig) *Identity {
	key := cfg.SigningKey
	if len(key) == 0 {
		key = []byte("tacplus-chaos-test-signing-key")
	}
	return &Identity{
		signingKey: key,
		now:        time.Now,
	}
}

// SetNow overrides the clock (tests use this for deterministic expiry).
func (id *Identity) SetNow(now func() time.Time) {
	id.mu.Lock()
	defer id.mu.Unlock()
	id.now = now
}

// SetRefreshFails toggles issuer availability. While true (the issuer is down),
// Issue and Refresh fail — and crucially, verification of an expired token must
// still fail closed rather than fall open.
func (id *Identity) SetRefreshFails(fails bool) {
	id.mu.Lock()
	defer id.mu.Unlock()
	id.refreshFails = fails
}

// SetClockSkew injects clock skew to exercise the token-expiry / skew fault.
func (id *Identity) SetClockSkew(skew time.Duration) {
	id.mu.Lock()
	defer id.mu.Unlock()
	id.clockSkew = skew
}

func (id *Identity) sign(t Token) string {
	mac := hmac.New(sha256.New, id.signingKey)
	fmt.Fprintf(mac, "%s|%t|%s|%d", t.Namespace, t.Entitled, t.Subject, t.NotAfter.UnixNano())
	return hex.EncodeToString(mac.Sum(nil))
}

// Issue mints a token for the given namespace/subject/entitlement. Returns an
// error if the issuer is down (refresh-failure fault).
func (id *Identity) Issue(namespace, subject string, entitled bool, ttl time.Duration) (Token, error) {
	id.mu.Lock()
	defer id.mu.Unlock()
	if id.refreshFails {
		return Token{}, fmt.Errorf("identity issuer unavailable")
	}
	if ttl == 0 {
		ttl = time.Minute
	}
	t := Token{
		Namespace: namespace,
		Subject:   subject,
		Entitled:  entitled,
		NotAfter:  id.now().Add(id.clockSkew).Add(ttl),
	}
	t.signature = id.sign(t)
	return t, nil
}

// Refresh re-issues a token. Fails closed when the issuer is down.
func (id *Identity) Refresh(t Token, ttl time.Duration) (Token, error) {
	return id.Issue(t.Namespace, t.Subject, t.Entitled, ttl)
}

// VerifyError categorizes a verification failure for the auth oracle.
type VerifyError struct{ Reason string }

func (e *VerifyError) Error() string { return "auth denied: " + e.Reason }

// Verify checks a token's signature, expiry, namespace, and entitlement against
// the required namespace and entitlement. It MUST fail closed: a tampered,
// expired, wrong-namespace, or unentitled token is denied even if the issuer is
// currently down. This is the negative oracle surface
// (archive/MODEL_STRESS_TEST_PLAN.md §2 / fault menu token-expiry row).
func (id *Identity) Verify(t Token, requireNamespace string, requireEntitled bool) error {
	id.mu.RLock()
	defer id.mu.RUnlock()

	if strings.TrimSpace(t.signature) == "" {
		return &VerifyError{Reason: "missing signature"}
	}
	if t.signature != id.sign(t) {
		return &VerifyError{Reason: "bad signature"}
	}
	if !id.now().Add(id.clockSkew).Before(t.NotAfter) {
		return &VerifyError{Reason: "expired"}
	}
	if requireNamespace != "" && t.Namespace != requireNamespace {
		return &VerifyError{Reason: "wrong namespace"}
	}
	if requireEntitled && !t.Entitled {
		return &VerifyError{Reason: "not entitled"}
	}
	return nil
}
