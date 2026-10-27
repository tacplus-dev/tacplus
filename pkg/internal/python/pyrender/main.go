// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Command pyrender renders the python jwk, token and spiffe chassis templates
// into a generated python model, for the jwk-alg gate (issue #179).
//
// Python is a client-only target, so a python build does not emit these
// packages; they are maintained but unreachable from generation. The gate still
// holds them to the shared alg-selection fixture, so it renders them here with
// the same data the python generator would pass.
//
//	go run ./pkg/internal/python/pyrender -package jwkalgclient_model -out <model>/jwkalgclient_model/chassis
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	commonspiffe "github.com/tacplus-dev/tacplus/pkg/internal/common/spiffe"
	"github.com/tacplus-dev/tacplus/templates"
)

func main() {
	pkg := flag.String("package", "", "python package base of the generated model")
	out := flag.String("out", "", "the generated model's chassis directory")
	flag.Parse()
	if *pkg == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "pyrender: -package and -out are required")
		os.Exit(2)
	}
	if err := render(*pkg, *out); err != nil {
		fmt.Fprintln(os.Stderr, "pyrender:", err)
		os.Exit(1)
	}
}

func render(pkg, out string) error {
	data := struct {
		PythonPackageBase string
		Spiffe            commonspiffe.TemplateGrammar
	}{PythonPackageBase: pkg, Spiffe: commonspiffe.TemplateData()}

	for _, dir := range []string{"jwk", "token", "spiffe"} {
		root := "python/chassis/" + dir
		err := fs.WalkDir(templates.FS, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
				return err
			}
			src, err := fs.ReadFile(templates.FS, path)
			if err != nil {
				return err
			}
			tmpl, err := template.New(path).Parse(string(src))
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			dest := filepath.Join(out, dir, strings.TrimSuffix(strings.TrimPrefix(path, root+"/"), ".tmpl"))
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dest, buf.Bytes(), 0o644)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
