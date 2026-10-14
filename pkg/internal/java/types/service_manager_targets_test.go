// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestJavaServiceManagerTargetsFollowGeneratedGrpcClients(t *testing.T) {
	cfg := &tactype.Config{
		GrpcS2SClients: []tactype.GrpcEntry{
			{
				ProtoFile:     "vservices/entitlements.proto",
				ProtoPackage:  "userentitlements",
				TargetService: "opa",
			},
		},
	}

	clientSvcs := []ServiceInfo{
		{
			ProtoFile:   "vservices/entitlements.proto",
			PackageName: "userentitlements",
			ServiceName: "OpaEval",
			ParentDir:   "vservices",
		},
		{
			ProtoFile:   "sys/eventbus.proto",
			PackageName: "eventbus",
			ServiceName: "Admin",
			ParentDir:   "services",
		},
	}

	targets := javaSmTargetServices(cfg, clientSvcs)
	got := make(map[string]string, len(targets))
	for _, target := range targets {
		got[target.ServiceID] = target.FieldName
	}

	for serviceID, fieldName := range map[string]string{
		"opa":      "opaConn",
		"eventbus": "eventbusConn",
	} {
		if got[serviceID] != fieldName {
			t.Fatalf("ServiceManager targets = %#v, missing %s/%s", targets, serviceID, fieldName)
		}
	}
	if len(targets) != 2 {
		t.Fatalf("ServiceManager should use the generated GrpcClients target set, got %#v", targets)
	}
}
