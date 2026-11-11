// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package publicsuffix

import (
	"errors"
	"slices"
	"testing"
)

func TestPunycodeRFC3492(t *testing.T) {
	for in, want := range map[string]string{
		"公司":      "55qx5d",
		"ελ":      "qxam",
		"bücher":  "bcher-kva",
		"рф":      "p1ai",
		"中国":      "fiqs8s",
		"ñandú":   "and-6ma2c",
		"日本語ドメイン": "eckwd4c7c5976acvb2w6i",
	} {
		got, err := punycode(in)
		if err != nil || got != want {
			t.Errorf("punycode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestRulesAreASCIISortedAndCoverBothSections(t *testing.T) {
	rs := Rules()
	if len(rs) < 9000 {
		t.Fatalf("only %d rules", len(rs))
	}
	if !slices.IsSorted(rs) {
		t.Fatal("rules not sorted")
	}
	for _, r := range rs {
		for i := 0; i < len(r); i++ {
			if r[i] >= 0x80 || r[i] >= 'A' && r[i] <= 'Z' {
				t.Fatalf("rule %q is not lowercase ASCII", r)
			}
		}
	}
	for _, want := range []string{"com", "co.uk", "github.io", "xn--55qx5d.cn", "*.ck", "!www.ck"} {
		if _, ok := slices.BinarySearch(rs, want); !ok {
			t.Errorf("rule %q missing", want)
		}
	}
	if Version() == "" {
		t.Error("the snapshot has no VERSION line")
	}
}

// The vectors every language's generated validator is held to.
func TestCheckRegistrable(t *testing.T) {
	for _, tc := range Vectors {
		if got := CheckRegistrable(tc.In); !errors.Is(got, tc.Want) {
			t.Errorf("CheckRegistrable(%q) = %v, want %v", tc.In, got, tc.Want)
		}
	}
	long := ""
	for len(long) < 260 {
		long += "abcdefghi."
	}
	if CheckRegistrable(long+"com") != ErrNotDomain {
		t.Error("a name over 253 bytes passed")
	}
}
