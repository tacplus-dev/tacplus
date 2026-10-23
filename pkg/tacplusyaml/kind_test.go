// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import "testing"

func TestCheckKind(t *testing.T) {
	for _, ok := range []string{"", "service", "client", "library"} {
		if err := CheckKind(ok); err != nil {
			t.Errorf("CheckKind(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"Service", "daemon", "lib"} {
		if err := CheckKind(bad); err == nil {
			t.Errorf("CheckKind(%q) passed", bad)
		}
	}
}
