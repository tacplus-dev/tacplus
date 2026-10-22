// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func TestGetMessageByNameRejectsCrossPackageSimpleNameCollision(t *testing.T) {
	g := &GoServerGenerator{
		ProtoInfo: &parser.ProtoInfo{
			Package: "owner",
			Messages: []*parser.MessageInfo{
				{Name: "Request"},
			},
		},
	}

	if got := g.getMessageByName("Request"); got == nil {
		t.Fatal("getMessageByName(simple) returned nil")
	}
	if got := g.getMessageByName("owner.Request"); got == nil {
		t.Fatal("getMessageByName(owner.Request) returned nil")
	}
	if got := g.getMessageByName("other.Request"); got != nil {
		t.Fatalf("getMessageByName(other.Request) = %#v, want nil", got)
	}
}
