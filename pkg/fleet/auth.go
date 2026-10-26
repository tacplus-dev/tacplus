// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/registry"
)

var fromFleetRe = regexp.MustCompile(`auth_services_from_fleet\s*:\s*true`)

// stampFleetAuth rewrites, in the merged tree, every (spec.service_s2s) that
// declares auth_services_from_fleet: its auth_services becomes the sorted set
// of fleet services that declare (spec.grpc_s2s_client) of it (or
// all_grpc_s2s_clients). The contract repo never carries one fleet's list; the
// tree each service is generated from does. A service nobody in the fleet
// calls keeps the flag and so admits no callers, which is the fail-closed
// answer, and is logged.
func stampFleetAuth(mergedDir string) error {
	root, cleanup, err := registry.PrepareDescriptorRoot(mergedDir)
	if err != nil {
		return fmt.Errorf("fleet: preparing descriptors: %w", err)
	}
	defer cleanup()
	dp, err := parser.NewDescriptorParser(root)
	if err != nil {
		return fmt.Errorf("fleet: parsing merged protos: %w", err)
	}
	files := dp.GetAllFiles()

	// Who calls whom, by "pkg.Service".
	callers := map[string]map[string]bool{}
	var callsAll []string
	for relPath, fd := range files {
		if len(fd.GrpcS2SClients) == 0 && !fd.AllGrpcS2SClients {
			continue
		}
		if fd.ServiceName == "" {
			return fmt.Errorf("fleet: %s declares an S2S client but no (spec.service) identity to admit as a caller", relPath)
		}
		if fd.AllGrpcS2SClients {
			callsAll = append(callsAll, fd.ServiceName)
		}
		for _, target := range fd.GrpcS2SClients {
			if callers[target] == nil {
				callers[target] = map[string]bool{}
			}
			callers[target][fd.ServiceName] = true
		}
	}

	for relPath, fd := range files {
		for i := range fd.Services {
			svc := &fd.Services[i]
			if !svc.AuthServicesFromFleet {
				continue
			}
			key := fd.Package + "." + svc.Name
			set := map[string]bool{}
			for who := range callers[key] {
				set[who] = true
			}
			for _, who := range callsAll {
				set[who] = true
			}
			if len(set) == 0 {
				zap.L().Warn("no service in the fleet declares a client of this service; it admits no callers", zap.String("service", key))
				continue
			}
			var list []string
			for who := range set {
				list = append(list, who)
			}
			sort.Strings(list)
			if err := rewriteAuthServices(filepath.Join(mergedDir, relPath), svc.Name, list); err != nil {
				return fmt.Errorf("fleet: stamping callers of %s: %w", key, err)
			}
			zap.L().Info("fleet caller list stamped", zap.String("service", key), zap.Strings("auth_services", list))
		}
	}
	return nil
}

// rewriteAuthServices replaces auth_services_from_fleet: true inside the named
// service's block with the literal list.
func rewriteAuthServices(path, serviceName string, list []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	start := strings.Index(text, "service "+serviceName+" {")
	if start < 0 {
		return fmt.Errorf("%s: service %s not found", path, serviceName)
	}
	open := start + len("service "+serviceName+" ")
	end := matchingBrace(text, open)
	if end < 0 {
		return fmt.Errorf("%s: service %s has no closing brace", path, serviceName)
	}
	body := text[open:end]
	if !fromFleetRe.MatchString(body) {
		return fmt.Errorf("%s: service %s declares no auth_services_from_fleet in its text", path, serviceName)
	}
	quoted := make([]string, len(list))
	for i, s := range list {
		quoted[i] = `"` + s + `"`
	}
	body = fromFleetRe.ReplaceAllLiteralString(body, "auth_services: ["+strings.Join(quoted, ", ")+"]")
	return os.WriteFile(path, []byte(text[:open]+body+text[end:]), 0o644)
}

// matchingBrace returns the index of the brace closing the one at open, or -1.
func matchingBrace(text string, open int) int {
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
