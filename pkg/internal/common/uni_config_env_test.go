// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// The uni-config is the config (tacplus#262, tacplus-dev/operator#17; Jody,
// 2026-11-02): a service never reads configuration that isn't declared in its
// config types and delivered from the uni-config, by file or by an env var the
// operator rendered from it. So the generated code reads the bootstrap config
// from the fixed BOOTSTRAP_CONFIG_PATH, the projected token file from the
// config's k8s_token_file, and a service's Secrets from SECRETS_PATH.
//
// This pins every environment read in the generated (non-test) templates of
// every language. A read of the whole environment is allowed only where it is
// filtered to the PROTO_* variables the config types declare. A read of a named
// variable is allowed only if it is listed below with the reason it is not
// deployment config. A new one fails here: put the value in the config types
// instead, or list it with its reason.
var envReadsNotConfig = map[string]string{
	// A token a parent workload writes for the child process it starts
	// (WorkloadMint): a process handoff, not deployment config.
	"bootstrapTokenFileEnv":    "parent workload handoff",
	"BOOTSTRAP_TOKEN_FILE_ENV": "parent workload handoff",

	// The cloud-native identity path reads the cloud provider's own workload
	// identity environment, which the provider sets, not tacplus config.
	"AWS_WEB_IDENTITY_TOKEN_FILE": "cloud provider workload identity",
	"AWS_ROLE_ARN":                "cloud provider workload identity",
	"AWS_REGION":                  "cloud provider workload identity",
	"AWS_DEFAULT_REGION":          "cloud provider workload identity",

	// The generated Rust SPIFFE vectors test, inside a non-test template.
	"SPIFFE_VECTOR_FIXTURE": "test fixture",

	// Not yet config; each is a PROPOSALS.md follow-up of tacplus#262.
	"LogLevelEnvVar":     "logger override, PROPOSALS.md",
	"LogFormatEnvVar":    "logger override, PROPOSALS.md",
	"LOG_LEVEL_ENV_VAR":  "logger override, PROPOSALS.md",
	"LOG_FORMAT_ENV_VAR": "logger override, PROPOSALS.md",
	"advertiseHostEnv":   "pod identity, PROPOSALS.md",
	"POD_NAME":           "pod identity, PROPOSALS.md",
	"HOSTNAME":           "pod identity, PROPOSALS.md",
}

// wholeEnvReaders are the templates that read the whole environment, each only
// to hand it to a generated <Config>FromEnvVar, which reads the PROTO_* keys
// the config types declare and nothing else.
var wholeEnvReaders = map[string]bool{
	"go/chassis/grpcconfig/grpcconfig.go.tmpl":           true,
	"java/chassis/grpcconfig/GrpcConfigLoader.java.tmpl": true,
	"java/types/ServiceManager.java.tmpl":                true,
	"rust/chassis/grpcconfig/src/loader.rs.tmpl":         true,
	"python/chassis/grpcconfig/loader.py.tmpl":           true,
	"typescript/chassis/grpcconfig/loader.ts.tmpl":       true,
}

// Each pattern's first non-empty group is the variable read; none means the
// whole environment.
var envReadPatterns = map[string][]*regexp.Regexp{
	".go.tmpl": {
		regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\(([^)]*)\)`),
		regexp.MustCompile(`os\.Environ\(\)()`),
	},
	".java.tmpl": {
		regexp.MustCompile(`System\.getenv\(([^)]*)\)`),
	},
	".rs.tmpl": {
		regexp.MustCompile(`env::var(?:_os)?\(([^)]*)\)`),
		regexp.MustCompile(`env::vars(?:_os)?\(\)()`),
	},
	".py.tmpl": {
		regexp.MustCompile(`os\.environ(?:\.get\(([^,)]+)|\[([^\]]+)\])?`),
		regexp.MustCompile(`os\.getenv\(([^,)]+)`),
	},
	".ts.tmpl": {
		regexp.MustCompile(`process\.env(?:\.([A-Za-z_]+)|\[([^\]]+)\])?`),
	},
}

func envVarName(arg string) string {
	arg = strings.Trim(strings.TrimSpace(arg), `"'`)
	if i := strings.LastIndex(arg, "."); i >= 0 {
		arg = arg[i+1:] // LoggerConstants.LOG_LEVEL_ENV_VAR
	}
	return arg
}

func TestGeneratedCodeReadsNoConfigFromTheEnvironment(t *testing.T) {
	var violations []string
	err := fs.WalkDir(templates.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		base := path[strings.LastIndex(path, "/")+1:]
		if strings.Contains(strings.ToLower(base), "test") {
			return nil // test templates read their own fixtures
		}
		var patterns []*regexp.Regexp
		for suffix, p := range envReadPatterns {
			if strings.HasSuffix(path, suffix) {
				patterns = p
			}
		}
		if patterns == nil {
			return nil
		}
		src, err := fs.ReadFile(templates.FS, path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			for _, re := range patterns {
				for _, m := range re.FindAllStringSubmatch(line, -1) {
					name := ""
					for _, g := range m[1:] {
						if g != "" {
							name = envVarName(g)
							break
						}
					}
					switch {
					case name == "":
						if !wholeEnvReaders[path] {
							violations = append(violations, path+": reads the whole environment: "+trimmed)
						}
					case envReadsNotConfig[name] == "":
						violations = append(violations, path+": reads "+name+" from the environment: "+trimmed)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v)
	}
	if len(violations) > 0 {
		t.Log("configuration comes from the uni-config: declare it in the config types " +
			"(grpcconfig.Config, or the service's Config/Secrets) instead of reading the environment")
	}
}
