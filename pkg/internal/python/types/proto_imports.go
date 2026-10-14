// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"path/filepath"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

func pyProtoFileModuleName(protoFile string) string {
	return pyModuleName(strings.TrimSuffix(filepath.Base(protoFile), ".proto"))
}

func pyProtoPackageDirName(protoPackage, protoFile string) string {
	return pythonProtoPackageName(protoPackage, pyProtoFileModuleName(protoFile))
}

func pyProtoImportPath(parentDir, protoPackage, protoFile string) string {
	return pyJoinDotted(pyDottedPath(parentDir), pyProtoPackageDirName(protoPackage, protoFile))
}

func pyCrossProtoFile(info *parser.ProtoInfo, protoPackage string) string {
	if info != nil && info.CrossPackageFiles != nil {
		if protoFile := info.CrossPackageFiles[protoPackage]; protoFile != "" {
			return protoFile
		}
	}
	dir := ""
	if info != nil && info.CrossPackageTypes != nil {
		dir = info.CrossPackageTypes[protoPackage]
	}
	fallback := pyProtoPackageLeaf(protoPackage) + ".proto"
	if dir == "" {
		return fallback
	}
	return filepath.ToSlash(filepath.Join(dir, fallback))
}

func pyCrossProtoImportPath(info *parser.ProtoInfo, protoPackage string) string {
	protoFile := pyCrossProtoFile(info, protoPackage)
	dir := ""
	if info != nil && info.CrossPackageTypes != nil {
		dir = info.CrossPackageTypes[protoPackage]
	}
	return pyProtoImportPath(dir, protoPackage, protoFile)
}

func pyProtoPackageLeaf(protoPackage string) string {
	parts := strings.Split(protoPackage, ".")
	return parts[len(parts)-1]
}

// pyExceptionsImportFrom returns the import base for the generated exceptions
// module. prefix is whatever leads to the python package root: a relative-import
// dot run ("....." ) for files nested under it, or "<PythonPackageBase>." for
// absolute imports. generateExceptions writes the module to
// types/sys/errors/exceptions.py (helpers_gen.go:128).
func pyExceptionsImportFrom(prefix string) string {
	return prefix + "types.sys.errors"
}
