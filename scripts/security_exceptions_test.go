// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// exceptionKey is an entry's field as the build pipeline's security checks
// (tacplus-dev/looper's looper-scan) read it: a strict subset of YAML, no
// inline comments (the rest of the line is the value).
var exceptionKey = regexp.MustCompile(`^(  - |    )([a-z_]+):(.*)$`)

// TestSecurityExceptions: the root security-exceptions.yaml (tacplus#340, the
// bridge of the first pinned build's findings) is in exactly the form the
// scanner accepts, else the build fails on the file before any finding: a list
// `exceptions:` of entries with exactly id, reason, decided_by and expires
// (YYYY-MM-DD, not past: an expired entry fails the build too), each id once;
// every entry of #340's bridge names, besides #340, the issue that removes it
// (Jody, #340). Other issues' entries (#376's triage) keep their own terms.
func TestSecurityExceptions(t *testing.T) {
	data, err := os.ReadFile("../security-exceptions.yaml")
	if err != nil {
		t.Fatalf("security-exceptions.yaml: %v", err)
	}
	var (
		seen    bool
		entries []map[string]string
		cur     map[string]string
	)
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line == "exceptions:" {
			seen = true
			continue
		}
		m := exceptionKey.FindStringSubmatch(line)
		if !seen || m == nil {
			t.Fatalf("security-exceptions.yaml:%d: not an exception entry: %q", n, line)
		}
		if m[1] == "  - " {
			cur = map[string]string{}
			entries = append(entries, cur)
		} else if cur == nil {
			t.Fatalf("security-exceptions.yaml:%d: a field before its entry: %q", n, line)
		}
		if _, dup := cur[m[2]]; dup {
			t.Fatalf("security-exceptions.yaml:%d: key %q twice in one entry", n, m[2])
		}
		cur[m[2]] = strings.Trim(strings.TrimSpace(m[3]), `"'`)
	}
	if len(entries) == 0 {
		t.Fatal("security-exceptions.yaml: no entries (delete the file instead)")
	}
	today := time.Now().UTC().Format("2006-01-02")
	issue := regexp.MustCompile(`#[0-9]+`)
	ids := map[string]bool{}
	for _, e := range entries {
		if len(e) != 4 || e["id"] == "" || e["reason"] == "" || e["decided_by"] == "" || e["expires"] == "" {
			t.Errorf("exception %q: needs exactly id, reason, decided_by and expires, has %v", e["id"], e)
			continue
		}
		if ids[e["id"]] {
			t.Errorf("exception %q: listed twice (the scanner takes the first)", e["id"])
		}
		ids[e["id"]] = true
		if _, err := time.Parse("2006-01-02", e["expires"]); err != nil {
			t.Errorf("exception %q: expires %q is not a date YYYY-MM-DD", e["id"], e["expires"])
		} else if e["expires"] < today {
			t.Errorf("exception %q expired on %s: fix the findings or renew it (a decision)", e["id"], e["expires"])
		}
		if !strings.Contains(e["decided_by"], "#340") && e["decided_by"] != "jodydadescott" {
			continue
		}
		if len(issue.FindAllString(strings.ReplaceAll(e["reason"], "#340", ""), -1)) == 0 {
			t.Errorf("exception %q: its reason names no issue (#N) that removes it", e["id"])
		}
	}
}
