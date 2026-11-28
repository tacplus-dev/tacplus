// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// rustConfigPushSlice is one pushed tenant slice this service owns
// (tacplus#927): the ServiceManager's receiver holds it, and its typed accessor
// reads it.
type rustConfigPushSlice struct {
	Field       int32  // the owner's registered tenant_slice_field, the slice's wire identity
	MessageName string // e.g. "KernelTenantSettings"
	FnName      string // the accessor, e.g. "kernel_tenant_settings"
	ModPath     string // the slice's types module, e.g. "crate::types::kernel::kernelsettings"
}

// rustConfigPushSlices returns the pushed tenant slices this service owns: the
// (spec.config_slice) tenant slices with delivery CONFIG_SLICE_DELIVERY_PUSH
// whose service_name is this service's label. None for a project that declares
// no package of its own, or with no flat proto dir.
func rustConfigPushSlices(config *tactype.Config) ([]rustConfigPushSlice, error) {
	if config.FlatProtoDir == "" || config.SelfPackage == "" {
		return nil, nil
	}
	owned, err := stateproto.OwnedPushedConfigSlices(config.InputDir, config.FlatProtoDir, config.ServiceLabel())
	if err != nil {
		return nil, err
	}
	var out []rustConfigPushSlice
	for _, s := range owned {
		out = append(out, rustConfigPushSlice{
			Field:       s.SliceField,
			MessageName: s.MessageName,
			FnName:      strutil.ToSnakeCase(s.MessageName),
			ModPath:     rustApprovalTypesModPath(s.Package, filepath.Dir(s.ProtoFile)),
		})
	}
	return out, nil
}

// generateConfigPush writes s2sm/config_push.rs, the receiver of the pushed
// tenant slices this service owns and their typed accessors, and
// s2sm/config_push_publisher.rs, a tenant's record store's publisher of its
// slices (tacplus#927). The ServiceManager declares `pub mod config_push;` and
// `pub mod config_push_publisher;` only when there are some.
func generateConfigPush(config *tactype.Config, data smTemplateData) error {
	if len(data.ConfigPushSlices) > 0 {
		if err := writeConfigPushModule(config, data, "config_push"); err != nil {
			return err
		}
	}
	if len(data.ConfigPushOwners) > 0 {
		return writeConfigPushModule(config, data, "config_push_publisher")
	}
	return nil
}

// writeConfigPushModule writes s2sm/<name>.rs from <name>.rs.tmpl.
func writeConfigPushModule(config *tactype.Config, data smTemplateData, name string) error {
	tmplContent, err := loadTemplate(name + ".rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load %s template: %w", name, err)
	}
	tmpl, err := template.New(name).Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse %s template: %w", name, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute %s template: %w", name, err)
	}
	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s dir: %w", name, err)
	}
	outputPath := filepath.Join(outputDir, name+".rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write %s.rs: %w", name, err)
	}
	zap.L().Info("Generated Rust config push module", zap.String("path", outputPath))
	return nil
}

// rustConfigPushFeed names the Watch<T>Changes of configpush.TenantSliceRecord
// on the record plane's S2S service a (spec.record_store).config_push store
// hosts (tacplus#927), which the ServiceManager feeds its publisher from.
type rustConfigPushFeed struct {
	Method     string // "watch_tenant_slice_record_changes"
	GetterName string // the Handler's getter of the plane's handler
	ModPath    string // the plane's types module
	Request    string // "WatchTenantSliceRecordChangesRequest"
}

// rustConfigPushFeedFor finds the plane's S2S service among the services the
// build hosts; a config_push store whose plane it does not host has nothing
// to watch, which is a generator error.
func rustConfigPushFeedFor(plane *stateproto.ConfigPushFeedPlane, entries []grpcServiceEntry) (*rustConfigPushFeed, error) {
	if plane == nil {
		return nil, nil
	}
	for _, e := range entries {
		if e.GrpcServiceName != plane.Package+"."+plane.S2SService &&
			e.GrpcServiceName != strings.SplitN(plane.Package, ".", 2)[0]+"."+plane.S2SService {
			continue
		}
		return &rustConfigPushFeed{
			Method:     "watch_" + strutil.ToSnakeCase(stateproto.TenantSliceRecordName) + "_changes",
			GetterName: e.GetterName,
			ModPath:    e.ModPath,
			Request:    "Watch" + stateproto.TenantSliceRecordName + "ChangesRequest",
		}, nil
	}
	return nil, fmt.Errorf("config push feed: the build hosts no %s.%s, the record plane configpush.TenantSliceRecord is watched on", plane.Package, plane.S2SService)
}
