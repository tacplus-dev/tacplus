// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import "testing"

func TestCheckDepends(t *testing.T) {
	for _, ok := range [][]string{nil, {"llmproxy"}, {"llmproxy", "weather@1.2"}, {"a-b_c@10.0"}} {
		if err := CheckDepends(ok); err != nil {
			t.Errorf("CheckDepends(%v) = %v, want pass", ok, err)
		}
	}
	for _, bad := range [][]string{{"llmproxy@1.2.3"}, {"LlmProxy"}, {"llmproxy", "llmproxy@1.0"}, {""}, {"llmproxy.LlmProxy"}} {
		if err := CheckDepends(bad); err == nil {
			t.Errorf("CheckDepends(%v) passed, want error", bad)
		}
	}
	if n, v := SplitDepend("weather@1.2"); n != "weather" || v != "1.2" {
		t.Errorf("SplitDepend = %q %q", n, v)
	}
	if n, v := SplitDepend("llmproxy"); n != "llmproxy" || v != "" {
		t.Errorf("SplitDepend = %q %q", n, v)
	}
}
