// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"
	"unicode"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
)

type tsClientIdentity struct {
	ParentDir   string
	PackageName string
	ServiceName string
}

type tsClientSymbol struct {
	ImportName string
	LocalName  string
	FieldName  string
}

func tsActualServicePackage(svc ServiceInfo) string {
	return svc.PackageName
}

func tsClientSymbols(ids []tsClientIdentity, suffix string) []tsClientSymbol {
	nameCounts := make(map[string]int)
	for _, id := range ids {
		nameCounts[id.ServiceName+suffix]++
	}

	usedLocalNames := make(map[string]int)
	usedFieldNames := make(map[string]int)
	symbols := make([]tsClientSymbol, 0, len(ids))
	for _, id := range ids {
		importName := id.ServiceName + suffix
		localName := importName
		fieldName := tsGeneratedSnakeName(id.ServiceName) + "_client"
		if nameCounts[importName] > 1 {
			prefix := tsClientQualifier(id)
			localName = prefix + importName
			fieldName = tsSnakeIdentifier(prefix) + "_" + tsGeneratedSnakeName(id.ServiceName) + "_client"
		}
		localName = tsUniqueIdentifier(localName, usedLocalNames)
		fieldName = tsUniqueIdentifier(fieldName, usedFieldNames)
		symbols = append(symbols, tsClientSymbol{
			ImportName: importName,
			LocalName:  localName,
			FieldName:  fieldName,
		})
	}
	return symbols
}

func tsClientQualifier(id tsClientIdentity) string {
	qualifier := tsPascalIdentifier(id.PackageName)
	if qualifier == "" {
		qualifier = tsPascalIdentifier(id.ParentDir)
	}
	if qualifier == "" {
		qualifier = "Client"
	}
	return qualifier
}

func tsNamedImport(name, localName string) string {
	if name == localName {
		return name
	}
	return fmt.Sprintf("%s as %s", name, localName)
}

func tsNamedExport(name, localName string) string {
	if name == localName {
		return name
	}
	return fmt.Sprintf("%s as %s", name, localName)
}

func tsUniqueIdentifier(name string, used map[string]int) string {
	if used[name] == 0 {
		used[name] = 1
		return name
	}
	used[name]++
	return fmt.Sprintf("%s%d", name, used[name])
}

func tsPascalIdentifier(parts ...string) string {
	words := tsIdentifierWords(parts...)
	if len(words) == 0 {
		return ""
	}
	return strutil.ToPascalCase(strings.Join(words, "_"))
}

func tsSnakeIdentifier(parts ...string) string {
	words := tsIdentifierWords(parts...)
	if len(words) == 0 {
		return ""
	}
	return strutil.ToSnakeCase(strings.Join(words, "_"))
}

func tsTonicLikeTypeName(protoName string) string {
	runes := []rune(protoName)
	n := len(runes)
	if n == 0 {
		return ""
	}

	const (
		modeBoundary = iota
		modeLowercase
		modeUppercase
	)

	var words []string
	mode := modeBoundary
	init := 0
	for i := 0; i < n; i++ {
		c := runes[i]
		if i+1 >= n {
			continue
		}
		next := runes[i+1]
		nextMode := mode
		if c >= 'a' && c <= 'z' {
			nextMode = modeLowercase
		} else if c >= 'A' && c <= 'Z' {
			nextMode = modeUppercase
		}
		if nextMode == modeLowercase && next >= 'A' && next <= 'Z' {
			words = append(words, string(runes[init:i+1]))
			init = i + 1
			mode = modeBoundary
			continue
		}
		if mode == modeUppercase && c >= 'A' && c <= 'Z' && next >= 'a' && next <= 'z' {
			if init < i {
				words = append(words, string(runes[init:i]))
			}
			init = i
			mode = modeBoundary
			continue
		}
		mode = nextMode
	}
	if init < n {
		words = append(words, string(runes[init:]))
	}

	var result strings.Builder
	for _, word := range words {
		first := true
		for _, r := range word {
			if first && r >= 'a' && r <= 'z' {
				result.WriteRune(r - 'a' + 'A')
				first = false
			} else if !first && r >= 'A' && r <= 'Z' {
				result.WriteRune(r - 'A' + 'a')
			} else {
				if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
					first = false
				}
				result.WriteRune(r)
			}
		}
	}
	return result.String()
}

func tsGeneratedSnakeName(name string) string {
	return strutil.ToSnakeCase(tsTonicLikeTypeName(name))
}

func tsIdentifierWords(parts ...string) []string {
	var words []string
	for _, part := range parts {
		for _, word := range strings.FieldsFunc(part, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			if word != "" {
				words = append(words, word)
			}
		}
	}
	return words
}
