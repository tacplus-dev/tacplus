// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestRecordC2SEmitsPhysicalEventBusRoute(t *testing.T) {
	protoDir := t.TempDir()
	if err := os.CopyFS(protoDir, os.DirFS("../../proto/testdata/eventbus_c2s_contract")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := protogen.CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(protoDir, protoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	if err := protogen.AppendEventBusWrappersToProtos(protoDir, protoDir); err != nil {
		t.Fatalf("AppendEventBusWrappersToProtos: %v", err)
	}
	if _, err := protogen.EnsureEventBusSupportProto(protoDir, protogen.EventBusSupportProtoOptions{}); err != nil {
		t.Fatalf("EnsureEventBusSupportProto: %v", err)
	}
	if err := protogen.AppendStateServiceToProtos(protoDir, protoDir); err != nil {
		t.Fatalf("AppendStateServiceToProtos: %v", err)
	}

	outDir := t.TempDir()
	config := &tactype.Config{OutputDir: outDir, IsGateway: true}
	services, err := detectServicesForWrapper(config, protoDir)
	if err != nil {
		t.Fatalf("detectServicesForWrapper: %v", err)
	}
	if err := generateRestDispatch(config, services, protoDir); err != nil {
		t.Fatalf("generateRestDispatch: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "s2sm", "rest_dispatch", "mod.rs"))
	if err != nil {
		t.Fatalf("read generated rest dispatch: %v", err)
	}
	rendered := string(data)
	grpcPath := `/tasks.DelegationProbeBusStateService/PublishToStream`
	if strings.Count(rendered, `grpc_path: "`+grpcPath+`"`) != 1 {
		t.Fatalf("generated ROUTES must contain exactly one %s entry:\n%s", grpcPath, rendered)
	}
	pathIndex := strings.Index(rendered, `grpc_path: "`+grpcPath+`"`)
	blockStart := strings.LastIndex(rendered[:pathIndex], "    RouteInfo {")
	blockEndOffset := strings.Index(rendered[pathIndex:], "    },")
	if blockStart < 0 || blockEndOffset < 0 {
		t.Fatalf("could not isolate generated route for %s:\n%s", grpcPath, rendered)
	}
	block := rendered[blockStart : pathIndex+blockEndOffset]
	for _, want := range []string{
		`service_id: "eventbus"`,
		`entitlement_service: Some("tasks")`,
		`server_streaming: true`,
		`client_streaming: true`,
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("generated route missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(rendered, `/tasks.DelegationProbeBusStateService/SubscribeToStream`) {
		t.Fatalf("generated ROUTES exposed SubscribeToStream for the publish-only C2S contract:\n%s", rendered)
	}
}
