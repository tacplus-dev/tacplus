// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// InjectFlockMetaFields rewrites the proto text in protoDir so every flock
// entry message (parser.FlockIndex, tacplus#206) gains the field
// `flock.Meta flock_meta = parser.FlockMetaFieldNumber;`: the merge metadata
// (LAST_WRITE stamps, set op sets, IMMUTABLE conflicts) that the generated
// merge reads and writes and that replicates with the entry. Injected before
// native compilation, it is in every language's generated type. An entry file
// already imports sys/flock.proto for its clock and mark, so no import is added.
func InjectFlockMetaFields(protoDir string) error {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for flock meta injection: %w", err)
	}
	all := dp.GetAllFiles()
	relPaths := make([]string, 0, len(all))
	for relPath := range all {
		if IsInfraProto(relPath) {
			continue
		}
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)
	files := make([]model.ProtoFile, 0, len(relPaths))
	fileOf := map[string]string{} // pkg + "\x00" + message -> relPath
	for _, relPath := range relPaths {
		pf, err := dp.ConvertToProtoFile(filepath.Join(protoDir, relPath))
		if err != nil {
			return fmt.Errorf("flock meta injection: parse %s: %w", relPath, err)
		}
		for _, m := range pf.Messages {
			fileOf[pf.Package+"\x00"+m.Name] = relPath
		}
		files = append(files, pf)
	}
	ix := parser.NewFlockIndex(files)
	perFile := map[string]map[string]bool{}
	for _, root := range ix.Roots {
		for _, b := range root.Boundaries {
			e := b.Entry
			relPath, ok := fileOf[e.Package+"\x00"+e.Message.Name]
			if !ok {
				return fmt.Errorf("flock meta injection: entry %s.%s is not in an authored proto", e.Package, e.Message.Name)
			}
			for _, f := range e.Message.Fields {
				if parser.IsFlockMetaField(f) {
					continue
				}
				if f.Name == parser.FlockMetaFieldName || f.Number == parser.FlockMetaFieldNumber {
					return fmt.Errorf("%s: flock entry %s already declares field %s = %d; tacplus adds flock.Meta %s = %d to it",
						relPath, e.Message.Name, f.Name, f.Number, parser.FlockMetaFieldName, parser.FlockMetaFieldNumber)
				}
			}
			if perFile[relPath] == nil {
				perFile[relPath] = map[string]bool{}
			}
			perFile[relPath][e.Message.Name] = true
		}
	}
	for relPath, msgs := range perFile {
		filePath := filepath.Join(protoDir, relPath)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read %s for flock meta injection: %w", relPath, err)
		}
		rewritten, err := injectFlockMetaText(string(content), msgs)
		if err != nil {
			return fmt.Errorf("%s: %w", relPath, err)
		}
		if err := os.WriteFile(filePath, []byte(rewritten), 0644); err != nil {
			return fmt.Errorf("write %s after flock meta injection: %w", relPath, err)
		}
	}
	return nil
}

// injectFlockMetaText adds the flock_meta declaration before the closing brace
// of each named top-level message (a "}" at column 0, as
// injectRedactIndicatorText finds it). A message that already has it is left
// alone, so the injection is idempotent.
func injectFlockMetaText(src string, msgs map[string]bool) (string, error) {
	decl := fmt.Sprintf("  flock.Meta %s = %d;", parser.FlockMetaFieldName, parser.FlockMetaFieldNumber)
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines)+3*len(msgs))
	found := map[string]bool{}
	current, has := "", false
	for _, line := range lines {
		if current == "" {
			if m := messageOpenRe.FindStringSubmatch(line); m != nil {
				current, has = m[1], false
			}
			out = append(out, line)
			continue
		}
		if strings.TrimSpace(line) == strings.TrimSpace(decl) {
			has = true
		}
		if strings.HasPrefix(line, "}") {
			if msgs[current] {
				if !has {
					out = append(out, "", "  // tacplus flock: the entry's merge metadata (tacplus#206).", decl)
				}
				found[current] = true
			}
			current = ""
		}
		out = append(out, line)
	}
	for name := range msgs {
		if !found[name] {
			return "", fmt.Errorf("message %s: could not locate its closing brace to inject %s", name, parser.FlockMetaFieldName)
		}
	}
	return strings.Join(out, "\n"), nil
}
