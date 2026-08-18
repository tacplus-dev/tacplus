// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"path/filepath"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
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

// pyExceptionsImportFrom returns the relative import base for the generated
// exceptions module, from a client file whose relative-import prefix is dots.
// generateExceptions writes it to types/sys/errors/exceptions.py.
func pyExceptionsImportFrom(dots string) string {
	return dots + "types.sys.errors"
}
