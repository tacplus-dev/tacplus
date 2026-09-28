// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// ValidateRestRootPath is the generation-time gate on REST_ROOT_PATH. Without
// it, two files declaring different values leave the resolved REST prefix up to
// protoregistry's undefined RangeFiles order, silently, at generation time.

// restRootPathFile builds one FileDescriptor declaring REST_ROOT_PATH with the
// given values, in the quoted form parseFileConstants produces.
func restRootPathFile(path string, values ...string) *FileDescriptor {
	fd := &FileDescriptor{Path: path, Package: "gateway"}
	for _, v := range values {
		fd.Constants = append(fd.Constants, model.Constant{
			Name:  "REST_ROOT_PATH",
			Type:  "string",
			Value: strconv.Quote(v),
		})
	}
	return fd
}

func restRootPathParser(files map[string]*FileDescriptor) *DescriptorParser {
	return &DescriptorParser{descriptors: files}
}

func TestValidateRestRootPathNilParser(t *testing.T) {
	if err := ValidateRestRootPath(nil); err != nil {
		t.Fatalf("ValidateRestRootPath(nil) = %v, want nil", err)
	}
}

func TestValidateRestRootPathAcceptsSingleAndRedundantDeclarations(t *testing.T) {
	cases := map[string]map[string]*FileDescriptor{
		"none declared": {
			"a.proto": {Path: "a.proto", Package: "gateway"},
		},
		"one file declares it": {
			"a.proto": restRootPathFile("a.proto", "/api/v1"),
			"b.proto": {Path: "b.proto", Package: "gateway"},
		},
		"two files declare the same value": {
			"a.proto": restRootPathFile("a.proto", "/api/v1"),
			"b.proto": restRootPathFile("b.proto", "/api/v1"),
		},
		"one file repeats the same value": {
			"a.proto": restRootPathFile("a.proto", "/api/v1", "/api/v1"),
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRestRootPath(restRootPathParser(files)); err != nil {
				t.Fatalf("ValidateRestRootPath rejected %s: %v", name, err)
			}
		})
	}
}

func TestValidateRestRootPathRejectsConflictingValues(t *testing.T) {
	err := ValidateRestRootPath(restRootPathParser(map[string]*FileDescriptor{
		"gateway.proto": restRootPathFile("gateway.proto", "/api/v1"),
		"legacy.proto":  restRootPathFile("legacy.proto", "/rest"),
	}))
	if err == nil {
		t.Fatal("ValidateRestRootPath accepted two different REST_ROOT_PATH values")
	}
	for _, want := range []string{"gateway.proto", "legacy.proto", `"/api/v1"`, `"/rest"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// The conflict the validator exists for arrives as two real .proto files in one
// tree, so exercise it through protoc and the descriptor parser, not only the IR.
func TestValidateRestRootPathConflictAcrossRealProtoFiles(t *testing.T) {
	skipIfNoProtoc(t)

	const header = `syntax = "proto3";

package gateway;

import "sys/spec.proto";

option (spec.string_const) = {name: "REST_ROOT_PATH", value: "%s"};

message %s { string m = 1; }
`

	t.Run("different values are rejected", func(t *testing.T) {
		dir := t.TempDir()
		copySpecProto(t, dir)
		writeTempProtoFile(t, dir, "gateway.proto", fmt.Sprintf(header, "/api/v1", "Ping"))
		writeTempProtoFile(t, dir, "legacy.proto", fmt.Sprintf(header, "/rest", "Pong"))
		p, err := NewDescriptorParser(dir)
		if err != nil {
			t.Fatalf("NewDescriptorParser: %v", err)
		}
		err = ValidateRestRootPath(p)
		if err == nil {
			t.Fatal("ValidateRestRootPath accepted a tree with two different REST_ROOT_PATH values")
		}
		for _, want := range []string{"gateway.proto", "legacy.proto", `"/api/v1"`, `"/rest"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})

	t.Run("identical values are accepted", func(t *testing.T) {
		dir := t.TempDir()
		copySpecProto(t, dir)
		writeTempProtoFile(t, dir, "gateway.proto", fmt.Sprintf(header, "/api/v1", "Ping"))
		writeTempProtoFile(t, dir, "legacy.proto", fmt.Sprintf(header, "/api/v1", "Pong"))
		p, err := NewDescriptorParser(dir)
		if err != nil {
			t.Fatalf("NewDescriptorParser: %v", err)
		}
		if err := ValidateRestRootPath(p); err != nil {
			t.Fatalf("ValidateRestRootPath rejected a redundant but identical declaration: %v", err)
		}
	})
}
