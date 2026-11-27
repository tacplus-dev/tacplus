// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

// generateConfigPush writes s2sm/config_push.rs: the receiver of the pushed
// tenant slices this service owns and their typed accessors (tacplus#927).
// The ServiceManager declares `pub mod config_push;` only when there are some.
func generateConfigPush(config *tactype.Config, data smTemplateData) error {
	if len(data.ConfigPushSlices) == 0 {
		return nil
	}
	tmplContent, err := loadTemplate("config_push.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load config_push template: %w", err)
	}
	tmpl, err := template.New("configpush").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse config_push template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute config_push template: %w", err)
	}
	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create config_push dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "config_push.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write config_push.rs: %w", err)
	}
	zap.L().Info("Generated Rust config push receiver", zap.String("path", outputPath))
	return nil
}
