// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"strings"
	"testing"
)

func TestCheckFloor(t *testing.T) {
	cases := []struct {
		floor, running string
		wantErr        string // substring; "" = pass
	}{
		{"", "0.0.0", ""},
		{"2.2", "v2.2.0", ""},
		{"2.2", "v2.2.7", ""},
		{"2.2", "v2.3.0", ""},
		{"2.2", "2.5.1-rc1", ""},
		{"2.2", "v2.1.9", "needs tacplus 2.2"},
		{"2.2", "v3.0.0", "needs tacplus 2.2"},
		{"2.2", "v1.9.0", "needs tacplus 2.2"},
		{"2.2.1", "v2.2.1", "never a patch pin"},
		{"v2.2", "v2.2.1", "must be MAJOR.MINOR"},
		{"2.2", "0.0.0", "unstamped"},
		{"2.2", "garbage", "unparseable"},
	}
	for _, c := range cases {
		err := CheckFloor(c.floor, c.running)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("CheckFloor(%q, %q) = %v, want pass", c.floor, c.running, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("CheckFloor(%q, %q) = %v, want error containing %q", c.floor, c.running, err, c.wantErr)
		}
	}
}
