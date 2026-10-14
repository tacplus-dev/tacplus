// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/keepaliveconst"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	commonspiffe "github.com/tacplus-dev/tacplus/pkg/internal/common/spiffe"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// amServiceRPC is the per-service RPC-enum accessor data the am.go.tmpl
// MethodLabel function needs — one entry per gRPC service in the fleet's
// generated AccountingMethod message.
type amServiceRPC struct {
	FieldName string // Go accessor name on *AccountingMethod, e.g. "Tasks"
}

// writeLibTemplates processes the embedded Go library templates and writes them
// to the output directory at {langDir}/sys/{libname}/
func writeLibTemplates(config *tactype.Config, acceptedAudiences []string, svcRPCs []proto.ServiceRPCEntry) error {
	langDir := config.LangDir()
	chassisDir := filepath.Join(langDir, "chassis")

	zap.L().Info("Writing library templates", zap.String("dir", chassisDir))

	// Does this proto set declare the durable accounting record? Client repos
	// and any build without platform/proto/vservices do not, and the chassis
	// EventBusSink is gated on it — it cannot name a type that was never
	// generated.
	hasAccountingRecord := false
	if config.AnnotatedProtoDir != "" {
		dp, err := parser.NewDescriptorParser(config.AnnotatedProtoDir)
		if err != nil {
			return fmt.Errorf("create descriptor parser for accounting record detection: %w", err)
		}
		_, hasAccountingRecord, err = boundaries.LookupAccountingRecordTopic(dp)
		if err != nil {
			return fmt.Errorf("resolve accounting record topic: %w", err)
		}
	}

	serviceRPCs := make([]amServiceRPC, 0, len(svcRPCs))
	for _, svc := range svcRPCs {
		serviceRPCs = append(serviceRPCs, amServiceRPC{FieldName: strutil.ToPascalCase(svc.GrpcName)})
	}

	// Template data
	data := struct {
		ModuleBasePath             string
		ServiceLabel               string   // service identity baked into Authorizer
		AcceptedAudiences          []string // every `aud` the Authorizer accepts: own label + hosted traits
		Spiffe                     commonspiffe.TemplateGrammar
		KeepaliveTimeSecs          int
		KeepaliveTimeoutSecs       int
		KeepalivePermitMinTimeSecs int
		HasAccountingRecord        bool // proto set declares accountingrecord.AccountingRecord
		ServiceRPCs                []amServiceRPC
	}{
		ModuleBasePath:             config.ModuleBasePath,
		ServiceLabel:               config.ServiceLabel(),
		AcceptedAudiences:          acceptedAudiences,
		Spiffe:                     commonspiffe.TemplateData(),
		KeepaliveTimeSecs:          keepaliveconst.TimeSecs,
		KeepaliveTimeoutSecs:       keepaliveconst.TimeoutSecs,
		KeepalivePermitMinTimeSecs: keepaliveconst.PermitMinTimeSecs,
		HasAccountingRecord:        hasAccountingRecord,
		ServiceRPCs:                serviceRPCs,
	}

	langFS, err := fs.Sub(templates.FS, "go/chassis")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	// Walk all embedded template files
	err = fs.WalkDir(langFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go.tmpl") {
			return nil
		}

		// Skip templates emitted by dedicated generators (not the generic walker).
		base := filepath.Base(path)
		// service_im and client_im require richer template data (the discovered
		// token-mint exchangers) than the generic chassis walker supplies, and
		// client_im is emitted only for client builds. They are emitted by
		// dedicated generators (generateIdentityManager /
		// generateClientIdentityManager), so skip them here.
		if base == "service_im.go.tmpl" || strings.HasPrefix(path, "client_im/") {
			return nil
		}

		// Read template content
		content, err := fs.ReadFile(langFS, path)
		if err != nil {
			return fmt.Errorf("failed to read template %s: %w", path, err)
		}

		// Parse and execute template
		tmpl, err := template.New(path).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", path, err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute template %s: %w", path, err)
		}

		// Output path: strip .tmpl suffix
		outputFile := strings.TrimSuffix(path, ".tmpl")
		outputPath := filepath.Join(chassisDir, outputFile)

		// Create directory
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", outputPath, err)
		}

		// Write file
		if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputPath, err)
		}

		zap.L().Debug("Wrote library file", zap.String("path", outputFile))
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to write library templates: %w", err)
	}

	zap.L().Info("Library templates written successfully")
	return nil
}
