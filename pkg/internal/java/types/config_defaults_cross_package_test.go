// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TestWriteTypeDefaults_CrossPackageSubMessageAppliesDefaults is the grpcconfig
// defect on the Java side: Config (package grpcconfig) holds a DiskPersistenceConfig
// from package diskstore, which carries defaults. Config's configDefaults must apply
// the diskstore defaults, or the nested archive defaults never reach validation.
func TestWriteTypeDefaults_CrossPackageSubMessageAppliesDefaults(t *testing.T) {
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
	cfg := &tactype.Config{JavaPackageBase: "com.example"}

	var b strings.Builder
	writeTypeDefaults(&b, grpcconfig, "Grpcconfig", cfg, messagesWithDefaults(allFiles))
	got := b.String()

	want := "builder.setDiskPersistence(com.example.types.sys.diskstore.Helpers.diskPersistenceConfigDefaults(msg.getDiskPersistence()).build());"
	if !strings.Contains(got, want) {
		t.Errorf("expected cross-package sub-message call %q, got:\n%s", want, got)
	}
}
