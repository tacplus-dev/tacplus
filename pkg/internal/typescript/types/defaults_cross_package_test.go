// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// TestWriteDefaults_CrossPackageSubMessageAppliesDefaults is the grpcconfig
// defect on the TypeScript side: Config (package grpcconfig) holds a
// DiskPersistenceConfig from package diskstore, which carries defaults. Config's
// configDefaults must call the diskstore defaults through an imported alias, or the
// nested archive defaults never reach validation.
func TestWriteDefaults_CrossPackageSubMessageAppliesDefaults(t *testing.T) {
	allFiles := []model.ProtoFile{
		{
			Package: "diskstore",
			Messages: []model.Message{
				{
					Name: "DiskPersistenceConfig",
					Fields: []model.Field{
						{
							Name: "roll_interval_seconds", Type: "int64",
							Annotations: map[string]model.AnnotationValue{"default_int": {IntValue: 86400}},
						},
					},
				},
			},
		},
		{
			Package: "grpcconfig",
			Imports: []model.Import{{Path: "sys/diskstore.proto", Package: "diskstore"}},
			Messages: []model.Message{
				{
					Name: "Config",
					Fields: []model.Field{
						{Name: "disk_persistence", Type: "DiskPersistenceConfig", TypePackage: "diskstore", IsMessage: true},
					},
				},
			},
		},
	}
	grpcconfig := allFiles[1]
	hasDefaults := messagesWithDefaults(allFiles)

	var b strings.Builder
	writeDefaults(&b, grpcconfig, hasDefaults)
	got := b.String()

	want := "if (m.diskPersistence) { diskstoreDiskPersistenceConfigDefaults(m.diskPersistence); }"
	if !strings.Contains(got, want) {
		t.Errorf("expected cross-package sub-message call %q, got:\n%s", want, got)
	}

	ctx := buildTsEnvVarContext(grpcconfig, hasDefaults)
	if alias := ctx.imports["diskstore"].funcAliases["diskPersistenceConfigDefaults"]; alias != "diskstoreDiskPersistenceConfigDefaults" {
		t.Errorf("expected import alias diskstoreDiskPersistenceConfigDefaults for the emitted call, got %q", alias)
	}
}
