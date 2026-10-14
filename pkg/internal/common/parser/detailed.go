// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ProtoFile represents a parsed proto file
type ProtoFile struct {
	Path     string
	Package  string
	Messages map[string]*ProtoMessage
	Enums    map[string]*ProtoEnum
	Services map[string]*ProtoService
}

// ProtoMessage represents a message definition
type ProtoMessage struct {
	Name   string
	Fields map[int]*ProtoField // key is field number
}

// ProtoField represents a field in a message
type ProtoField struct {
	Number   int
	Name     string
	Type     string
	Repeated bool
	Optional bool
}

// ProtoEnum represents an enum definition
type ProtoEnum struct {
	Name   string
	Values map[int]string // key is enum value number
}

// ProtoService represents a service definition
type ProtoService struct {
	Name string
	RPCs map[string]*ProtoRPC
}

// ProtoRPC represents an RPC method
type ProtoRPC struct {
	Name       string
	InputType  string
	OutputType string
}

// ParseProtoDirectoryDetailed parses all .proto files in a directory recursively.
func ParseProtoDirectoryDetailed(dir string) (map[string]*ProtoFile, error) {
	protos := make(map[string]*ProtoFile)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(path, ".proto") {
			relPath, _ := filepath.Rel(dir, path)
			proto, err := ParseProtoFileDetailed(path)
			if err != nil {
				return err
			}
			proto.Path = relPath
			protos[relPath] = proto
		}

		return nil
	})

	return protos, err
}

// ParseProtoFileDetailed parses a single proto file using regex-based extraction.
// Uses strings.Contains on raw proto text for syntax structure detection (braces,
// equals signs, keywords). This is a text-level parser — no descriptors available.
func ParseProtoFileDetailed(path string) (*ProtoFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	proto := &ProtoFile{
		Path:     path,
		Messages: make(map[string]*ProtoMessage),
		Enums:    make(map[string]*ProtoEnum),
		Services: make(map[string]*ProtoService),
	}

	scanner := bufio.NewScanner(file)
	var currentMessage *ProtoMessage
	var currentEnum *ProtoEnum
	var currentService *ProtoService
	var braceDepth int

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Skip comments and empty lines
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		// Remove inline comments
		if idx := strings.Index(trimmed, "//"); idx != -1 {
			trimmed = strings.TrimSpace(trimmed[:idx])
		}

		// Package declaration
		if strings.HasPrefix(trimmed, "package ") {
			proto.Package = strings.TrimSuffix(strings.TrimSpace(trimmed[8:]), ";")
			continue
		}

		// Track brace depth
		braceDepth += strings.Count(trimmed, "{")
		braceDepth -= strings.Count(trimmed, "}")

		// Message start
		if strings.HasPrefix(trimmed, "message ") && strings.Contains(trimmed, "{") {
			messageName := extractName(trimmed, "message")
			if messageName != "" && currentMessage == nil {
				currentMessage = &ProtoMessage{
					Name:   messageName,
					Fields: make(map[int]*ProtoField),
				}
			}
			continue
		}

		// Enum start
		if strings.HasPrefix(trimmed, "enum ") && strings.Contains(trimmed, "{") {
			enumName := extractName(trimmed, "enum")
			if enumName != "" && currentEnum == nil {
				currentEnum = &ProtoEnum{
					Name:   enumName,
					Values: make(map[int]string),
				}
			}
			continue
		}

		// Service start
		if strings.HasPrefix(trimmed, "service ") && strings.Contains(trimmed, "{") {
			serviceName := extractName(trimmed, "service")
			if serviceName != "" && currentService == nil {
				currentService = &ProtoService{
					Name: serviceName,
					RPCs: make(map[string]*ProtoRPC),
				}
			}
			continue
		}

		// Field in message
		if currentMessage != nil && braceDepth == 1 {
			field := parseField(trimmed)
			if field != nil {
				currentMessage.Fields[field.Number] = field
			}
		}

		// Value in enum
		if currentEnum != nil && braceDepth == 1 {
			name, value := parseEnumValue(trimmed)
			if name != "" {
				currentEnum.Values[value] = name
			}
		}

		// RPC in service
		if currentService != nil && braceDepth == 1 {
			rpc := parseRPC(trimmed)
			if rpc != nil {
				currentService.RPCs[rpc.Name] = rpc
			}
		}

		// End of message/enum/service
		if braceDepth == 0 {
			if currentMessage != nil {
				proto.Messages[currentMessage.Name] = currentMessage
				currentMessage = nil
			}
			if currentEnum != nil {
				proto.Enums[currentEnum.Name] = currentEnum
				currentEnum = nil
			}
			if currentService != nil {
				proto.Services[currentService.Name] = currentService
				currentService = nil
			}
		}
	}

	return proto, scanner.Err()
}

// extractName extracts the name after a keyword
func extractName(line, keyword string) string {
	re := regexp.MustCompile(keyword + `\s+(\w+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

// parseField parses a field definition
func parseField(line string) *ProtoField {
	// Match: [repeated] [optional] type name = number [options];
	line = strings.TrimSuffix(line, ";")

	if !strings.Contains(line, "=") {
		return nil
	}

	parts := strings.Fields(line)
	if len(parts) < 4 {
		return nil
	}

	field := &ProtoField{}

	idx := 0
	// Check for repeated/optional
	if parts[idx] == "repeated" {
		field.Repeated = true
		idx++
	} else if parts[idx] == "optional" {
		field.Optional = true
		idx++
	}

	if idx+2 >= len(parts) {
		return nil
	}

	// Type and name
	field.Type = parts[idx]
	field.Name = parts[idx+1]
	idx += 2

	// Skip '=' if it's a separate token
	if idx < len(parts) && parts[idx] == "=" {
		idx++
	}

	// Field number
	if idx < len(parts) {
		numStr := strings.TrimPrefix(parts[idx], "=")
		numStr = strings.Split(numStr, "[")[0] // Remove options
		numStr = strings.TrimSpace(numStr)
		if num, err := strconv.Atoi(numStr); err == nil {
			field.Number = num
		}
	}

	if field.Number == 0 {
		return nil
	}

	return field
}

// parseEnumValue parses an enum value
func parseEnumValue(line string) (string, int) {
	// Match: NAME = number;
	line = strings.TrimSuffix(line, ";")

	if !strings.Contains(line, "=") {
		return "", 0
	}

	parts := strings.Split(line, "=")
	if len(parts) != 2 {
		return "", 0
	}

	name := strings.TrimSpace(parts[0])
	valueStr := strings.TrimSpace(parts[1])
	valueStr = strings.Split(valueStr, "[")[0] // Remove options
	valueStr = strings.TrimSpace(valueStr)

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return "", 0
	}

	return name, value
}

// parseRPC parses an RPC definition
func parseRPC(line string) *ProtoRPC {
	// Match: rpc Name(InputType) returns (OutputType);
	if !strings.HasPrefix(line, "rpc ") {
		return nil
	}

	re := regexp.MustCompile(`rpc\s+(\w+)\s*\(([^)]+)\)\s*returns\s*\(([^)]+)\)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) != 4 {
		return nil
	}

	return &ProtoRPC{
		Name:       matches[1],
		InputType:  strings.TrimSpace(matches[2]),
		OutputType: strings.TrimSpace(matches[3]),
	}
}
