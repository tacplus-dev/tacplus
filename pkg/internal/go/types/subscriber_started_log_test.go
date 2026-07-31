// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"strings"
	"testing"
)

// Cross-language observability parity: every generated subscriber must log when
// its subscription is ESTABLISHED, not only on error/EOF/cancel. The go template
// previously logged nothing on success, so a healthy subscriber looked identical
// to a dead one — silence after transient-error retries actually meant success.
// That cost a live misdiagnosis (a graphdb subscriber was reported abandoned
// while it had an active Kafka consumer group), and it made an e2e check that
// greps for the started-line unsatisfiable for go services.
func TestSubscriberTemplatesLogSubscriptionStarted(t *testing.T) {
	cases := []struct {
		lang, path, want string
	}{
		{"go", "../../../templates/go/types/subscriber.go.tmpl", "subscription stream started"},
		{"rust", "../../../templates/rust/types/subscriber.rs.tmpl", "subscription stream started"},
	}
	for _, c := range cases {
		data, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("%s: read template: %v", c.lang, err)
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("%s subscriber template does not log %q on success — a healthy subscriber would be indistinguishable from a dead one", c.lang, c.want)
		}
	}
}
