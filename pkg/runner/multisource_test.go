// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"reflect"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

func TestEffectiveSources(t *testing.T) {
	tests := []struct {
		name string
		p    Params
		cfg  *tacplusyaml.Config
		want []string
	}{
		{
			name: "explicit --src overrides yaml entirely",
			p:    Params{ProtoSrcs: []string{"/flag/a", "/flag/b"}, SrcExplicit: true},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary", Sources: []string{"/yaml/extra"}},
			want: []string{"/flag/a", "/flag/b"},
		},
		{
			name: "yaml src + sources union when not explicit",
			p:    Params{},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary", Sources: []string{"/yaml/kernel", "/yaml/base"}},
			want: []string{"/yaml/primary", "/yaml/kernel", "/yaml/base"},
		},
		{
			name: "yaml src only (back-compat single source)",
			p:    Params{},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary"},
			want: []string{"/yaml/primary"},
		},
		{
			name: "empty everything -> nil (sys-only)",
			p:    Params{},
			cfg:  &tacplusyaml.Config{},
			want: nil,
		},
		{
			name: "empty flag entries filtered out",
			p:    Params{ProtoSrcs: []string{"", "/flag/a", ""}, SrcExplicit: true},
			cfg:  nil,
			want: []string{"/flag/a"},
		},
		{
			name: "explicit but no flag values -> nil (sys-only), yaml ignored",
			p:    Params{SrcExplicit: true},
			cfg:  &tacplusyaml.Config{Src: "/yaml/primary"},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveSources(tt.p, tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("effectiveSources() = %v, want %v", got, tt.want)
			}
		})
	}
}
