// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

// A grpcconfig-shaped pair: Config (package grpcconfig) holds a DiskPersistenceConfig
// from package diskstore, which holds an ArchiveConfig. Only the diskstore messages
// carry defaults of their own.
func grpcconfigShapedFiles() []ProtoFile {
	return []ProtoFile{
		{
			Package: "diskstore",
			Messages: []Message{
				{Name: "DiskPersistenceConfig", Fields: []Field{
					{Name: "archive", Type: "ArchiveConfig", IsMessage: true},
				}},
				{Name: "ArchiveConfig"},
			},
		},
		{
			Package: "grpcconfig",
			Messages: []Message{
				{Name: "Config", Fields: []Field{
					{Name: "disk_persistence", Type: "DiskPersistenceConfig", TypePackage: "diskstore", IsMessage: true},
				}},
				{Name: "Plain", Fields: []Field{
					{Name: "count", Type: "int64"},
				}},
			},
		},
	}
}

func TestExpandDefaultsReachesAcrossPackages(t *testing.T) {
	direct := map[string]bool{
		DefaultsKey("diskstore", "DiskPersistenceConfig"): true,
		DefaultsKey("diskstore", "ArchiveConfig"):         true,
	}

	got := ExpandDefaults(grpcconfigShapedFiles(), direct)

	for _, key := range []struct{ pkg, name string }{
		{"diskstore", "DiskPersistenceConfig"},
		{"diskstore", "ArchiveConfig"},
		{"grpcconfig", "Config"},
	} {
		if !got[DefaultsKey(key.pkg, key.name)] {
			t.Errorf("%s.%s: want defaults via cross-package field, got none", key.pkg, key.name)
		}
	}
	if got[DefaultsKey("grpcconfig", "Plain")] {
		t.Errorf("grpcconfig.Plain: want no defaults, got some")
	}
}

func TestExpandDefaultsSamePackageNameDoesNotCollide(t *testing.T) {
	// A message named Config in another package must not be reached through
	// a field whose type is Config in grpcconfig.
	files := []ProtoFile{
		{Package: "other", Messages: []Message{{Name: "Config"}}},
		{Package: "grpcconfig", Messages: []Message{
			{Name: "Holder", Fields: []Field{{Name: "c", Type: "Config", IsMessage: true}}},
		}},
	}
	direct := map[string]bool{DefaultsKey("other", "Config"): true}

	got := ExpandDefaults(files, direct)

	if got[DefaultsKey("grpcconfig", "Holder")] {
		t.Errorf("grpcconfig.Holder reached other.Config through a same-package field; want no defaults")
	}
}

func TestExpandDefaultsSkipsRepeatedAndMapFields(t *testing.T) {
	files := []ProtoFile{
		{Package: "diskstore", Messages: []Message{{Name: "ArchiveConfig"}}},
		{Package: "grpcconfig", Messages: []Message{
			{Name: "Listy", Fields: []Field{{Name: "a", Type: "ArchiveConfig", TypePackage: "diskstore", IsMessage: true, IsRepeated: true}}},
			{Name: "Mappy", Fields: []Field{{Name: "m", Type: "ArchiveConfig", TypePackage: "diskstore", IsMessage: true, IsMap: true}}},
		}},
	}
	direct := map[string]bool{DefaultsKey("diskstore", "ArchiveConfig"): true}

	got := ExpandDefaults(files, direct)

	for _, name := range []string{"Listy", "Mappy"} {
		if got[DefaultsKey("grpcconfig", name)] {
			t.Errorf("grpcconfig.%s: repeated or map field must not expand defaults", name)
		}
	}
}
