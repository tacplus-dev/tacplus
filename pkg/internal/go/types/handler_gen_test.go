// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestBuildServiceManagerDataDisambiguatesWrapperAliases(t *testing.T) {
	cfg := &tactype.Config{
		ModuleBasePath: "example.com/project/tacplus",
	}
	services := []ServiceInfo{
		{
			ProtoFile:   "contract/contract.proto",
			PackageName: "contractclient",
			ServiceName: "ContractApi",
			ParentDir:   "contract",
			Config:      cfg,
		},
		{
			ProtoFile:   "services/contractaudit.proto",
			PackageName: "contractclient",
			ServiceName: "ContractAuditApi",
			ParentDir:   "services",
			Config:      cfg,
		},
	}

	data, err := buildServiceManagerData(cfg, services, nil)
	if err != nil {
		t.Fatalf("buildServiceManagerData: %v", err)
	}
	if len(data.WrapperImports) != 2 {
		t.Fatalf("wrapper imports = %d, want 2: %#v", len(data.WrapperImports), data.WrapperImports)
	}

	aliases := map[string]string{}
	for _, imp := range data.WrapperImports {
		aliases[imp.ImportPath] = imp.Alias
	}
	if aliases["example.com/project/tacplus/s2sm/grpc/servers/contract/contractclient"] != "contract_contractclient_fe" {
		t.Fatalf("contract wrapper alias = %q", aliases["example.com/project/tacplus/s2sm/grpc/servers/contract/contractclient"])
	}
	if aliases["example.com/project/tacplus/s2sm/grpc/servers/services/contractclient"] != "services_contractclient_fe" {
		t.Fatalf("services wrapper alias = %q", aliases["example.com/project/tacplus/s2sm/grpc/servers/services/contractclient"])
	}
}

func TestGoPublisherEntryUsesDeclaredBrokerPath(t *testing.T) {
	cfg := &tactype.Config{
		ModuleBasePath: "example.com/project/tacplus",
		SelfPackage:    "contractfixtures",
	}
	entry := goPublisherEntryFromData(cfg, boundaries.EventPublisherData{
		TopicRef:          "contractfixtures.FixtureState.events",
		BrokerLabel:       "contractfixtures",
		EventTypeFQN:      "contractfixtures.FixtureEvent",
		EventTypeName:     "FixtureEvent",
		RPCStem:           "ContractfixturesFixtureStateEvents",
		PublishStreamName: "PublishContractfixturesFixtureStateEvents",
		PublishPath:       "/contractfixtures.EventBusTypedService/PublishContractfixturesFixtureStateEvents",
	}, map[string]string{"contractfixtures": "services"})

	if entry.MethodName != "CreateContractfixturesFixtureStateEventsPublisher" {
		t.Fatalf("MethodName = %q", entry.MethodName)
	}
	if entry.TargetService != "contractfixtures" {
		t.Fatalf("TargetService = %q", entry.TargetService)
	}
	if entry.PublishPath != "/contractfixtures.EventBusTypedService/PublishContractfixturesFixtureStateEvents" {
		t.Fatalf("PublishPath = %q", entry.PublishPath)
	}
	if entry.TypePkgAlias != "contractfixtures_pb" {
		t.Fatalf("TypePkgAlias = %q", entry.TypePkgAlias)
	}
	if entry.TypePkgImport != "example.com/project/tacplus/types/services/contractfixtures" {
		t.Fatalf("TypePkgImport = %q", entry.TypePkgImport)
	}
}
