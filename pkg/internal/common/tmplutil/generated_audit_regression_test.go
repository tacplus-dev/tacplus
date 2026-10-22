// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tmplutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedoLogTemplatesRecoverBuildingSnapshot(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "go",
			path: "pkg/templates/go/chassis/diskstore/redolog.go.tmpl",
			want: []string{
				`building := sibling(l.active, ".building")`,
				"if fileExists(building)",
				"promote(building, l.active)",
				`os.OpenFile(l.active, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)`,
			},
		},
		{
			name: "typescript",
			path: "pkg/templates/typescript/chassis/diskstore/redolog.ts.tmpl",
			want: []string{
				`const building = sibling(this.active, '.building')`,
				"if (fs.existsSync(building))",
				"promote(building, this.active)",
				"this.writer = openAppend(this.active)",
			},
		},
		{
			name: "python",
			path: "pkg/templates/python/chassis/diskstore/redolog.py.tmpl",
			want: []string{
				`building = self._active + ".building"`,
				"if os.path.exists(building):",
				"promote(building, self._active)",
				`self._writer = open(self._active, "ab")`,
			},
		},
		{
			name: "java",
			path: "pkg/templates/java/chassis/diskstore/RedoLog.java.tmpl",
			want: []string{
				`Path building = FsUtil.sibling(active, ".building")`,
				"if (Files.exists(building))",
				"FsUtil.promote(building, active)",
				"writer = openAppend(active)",
			},
		},
		{
			name: "rust",
			path: "pkg/templates/rust/chassis/diskstore/src/redolog.rs.tmpl",
			want: []string{
				`let building = sibling(&self.active, ".building")`,
				"if building.exists()",
				"promote(&building, &self.active)",
				"OpenOptions::new().create(true).append(true).open(&self.active)",
				`DiskStoreError::general("redo log writer is closed")`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := readTemplateForAuditTest(t, root, tt.path)
			for _, want := range tt.want {
				if !strings.Contains(src, want) {
					t.Fatalf("%s missing %q:\n%s", tt.path, want, src)
				}
			}
		})
	}
}

func TestDiskstoreFsutilTemplatesFsyncRenameDirectories(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "go",
			path: "pkg/templates/go/chassis/diskstore/fsutil.go.tmpl",
			want: []string{
				"func promote(tmp, path string) error",
				"if err := os.Rename(tmp, path); err != nil",
				"syncDir(filepath.Dir(path))",
				"func rotateArchives(path string, count uint32) error",
				"_ = os.Remove(path)\n\t\tsyncDir(filepath.Dir(path))",
				"if fileExists(path)",
				"syncDir(filepath.Dir(path))\n\treturn nil",
			},
		},
		{
			name: "typescript",
			path: "pkg/templates/typescript/chassis/diskstore/fsutil.ts.tmpl",
			want: []string{
				"export function promote(tmp: string, target: string): void",
				"fs.renameSync(tmp, target)",
				"syncDir(path.dirname(target));",
				"export function rotateArchives(target: string, count: number): void",
				"removeIfExists(target);\n    syncDir(dir);",
				"if (fs.existsSync(target))",
				"syncDir(dir);\n}",
			},
		},
		{
			name: "python",
			path: "pkg/templates/python/chassis/diskstore/fsutil.py.tmpl",
			want: []string{
				"def promote(tmp: str, path: str) -> None:",
				"os.replace(tmp, path)",
				"_sync_dir(parent)",
				"def rotate_archives(path: str, count: int) -> None:",
				"_remove_if_exists(path)\n            if parent:",
				"if os.path.exists(path):",
				"if parent:\n            _sync_dir(parent)",
			},
		},
		{
			name: "java",
			path: "pkg/templates/java/chassis/diskstore/FsUtil.java.tmpl",
			want: []string{
				"public static void promote(Path tmp, Path path)",
				"Files.move(tmp, path,",
				"syncDir(dir);",
				"public static void rotateArchives(Path path, int count)",
				"Files.deleteIfExists(path);\n                if (dir != null)",
				"if (Files.exists(path))",
				"if (dir != null) {\n                syncDir(dir);",
			},
		},
		{
			name: "rust",
			path: "pkg/templates/rust/chassis/diskstore/src/fsutil.rs.tmpl",
			want: []string{
				"pub fn promote(tmp: &Path, path: &Path) -> Result<()>",
				"fs::rename(tmp, path)?;",
				"sync_dir(dir);",
				"pub fn rotate_archives(path: &Path, count: u32) -> Result<()>",
				"let _ = fs::remove_file(path);\n        if let Some(dir) = dir.as_deref()",
				"if path.exists()",
				"if let Some(dir) = dir.as_deref() {\n        sync_dir(dir);",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := readTemplateForAuditTest(t, root, tt.path)
			for _, want := range tt.want {
				if !strings.Contains(src, want) {
					t.Fatalf("%s missing %q:\n%s", tt.path, want, src)
				}
			}
		})
	}
}

func TestRustDiskstoreSchedulerUsesExistingLogDependency(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	src := readTemplateForAuditTest(t, root, "pkg/templates/rust/chassis/diskstore/src/schedule.rs.tmpl")

	for _, want := range []string{
		"log::warn!",
		"diskstore roll failed; retrying on next interval",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Rust diskstore schedule template missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "use tracing::warn") || strings.Contains(src, "tracing::warn!") {
		t.Fatalf("Rust diskstore schedule template should use the existing log dependency, not tracing:\n%s", src)
	}
}

func TestRustGrpcServerTemplateHasCompilableDefaults(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	src := readTemplateForAuditTest(t, root, "pkg/templates/rust/chassis/grpcserver/src/lib.rs.tmpl")

	for _, want := range []string{
		"impl Default for TLSConfig",
		"tls: Some(TLSConfig::default())",
		"alpn_protocols: vec![ALPNProtocol::H2, ALPNProtocol::Http11]",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Rust grpcserver template missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "routing::get") {
		t.Fatalf("Rust grpcserver template imports unused routing::get:\n%s", src)
	}
}

func TestJavaServiceManagerGuardsHealthStateOnlyHelpers(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	src := readTemplateForAuditTest(t, root, "pkg/templates/java/types/ServiceManager.java.tmpl")

	for _, want := range []string{
		"{{- if or .HasServiceHealthState .HasStates .HasTracePublisher}}\nimport {{.TypesBase}}.sys.trace.Trace;",
		"{{- if .HasTracePublisher}}\n        // Wire trace publisher to all wrappers.",
		"{{- if .HasServiceHealthState}}\n    /**\n     * Resolves a service endpoint by querying ServiceHealthState",
		"private static HealthReporter.MetricsProvider buildMetricsProvider",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Java service manager template missing guarded health-state fragment %q:\n%s", want, src)
		}
	}
}

func TestJavaHandlerTemplateGuardsConfigCallbacks(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	src := readTemplateForAuditTest(t, root, "pkg/templates/java/types/Handler.java.tmpl")

	for _, want := range []string{
		"{{- if .HasConfigState}}\nimport {{.ConfigImport}};",
		"{{- if .HasConfigState}}\n    /**\n     * Called with the initial typed config before the server starts,",
		"void {{.HandleConfigName}}({{.ConfigType}} cfg) throws Exception;",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Java handler template missing config-state guard fragment %q:\n%s", want, src)
		}
	}
}

func TestGoServiceManagerFailsFastWhenAuthWrapperIsUnwired(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	wrapper := readTemplateForAuditTest(t, root, "pkg/templates/go/types/servicewrapper.go.tmpl")
	manager := readTemplateForAuditTest(t, root, "pkg/templates/go/types/service_manager.go.tmpl")

	for _, want := range []string{
		"func (p *{{.ServerName}}) HasAuthorizer() bool",
		"return p.authorizeFunc != nil && p.authenticateFunc != nil",
	} {
		if !strings.Contains(wrapper, want) {
			t.Fatalf("Go service wrapper template missing authorizer invariant fragment %q:\n%s", want, wrapper)
		}
	}
	for _, want := range []string{
		"{{.WrapperVarName}}.SetAuthenticateFunc(sm.authenticateFunc)",
		"{{.WrapperVarName}}.SetAuthorizeFunc(sm.authorizeFunc)",
		"if !{{.WrapperVarName}}.HasAuthorizer() {",
		"mandatory-auth service cannot serve without an authorizer",
	} {
		if !strings.Contains(manager, want) {
			t.Fatalf("Go service manager template missing fail-fast auth fragment %q:\n%s", want, manager)
		}
	}
	if strings.Contains(manager, "TODO(#32)") {
		t.Fatalf("Go service manager template still carries the completed auth TODO:\n%s", manager)
	}
}

func TestServiceManagersWireMixedAuthWrappersBeforeServing(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	tests := []struct {
		name    string
		path    string
		want    []string
		forbid  []string
		message string
	}{
		{
			name: "go",
			path: "pkg/templates/go/types/service_manager.go.tmpl",
			want: []string{
				"{{.WrapperVarName}}.SetAuthorizeFunc(sm.authorizeFunc)",
				"{{.WrapperVarName}}.SetAuthenticateFunc(sm.authenticateFunc)",
				"{{.WrapperVarName}}.SetAuthenticateAtOrBelowFunc(sm.authenticateAtOrBelowFunc)",
				"if !{{.WrapperVarName}}.HasAuthorizer() {",
			},
			forbid: []string{
				"{{- if .AuthAnyUser}}\n{{.WrapperVarName}}.SetAuthenticateFunc",
			},
			message: "Go ServiceManager must wire both auth callbacks so mixed authorize/auth_any_user services do not fail normal RPCs",
		},
		{
			name: "python_service_manager",
			path: "pkg/templates/python/types/service_manager.py.tmpl",
			want: []string{
				"from {{.PythonPackageBase}}.chassis.grpcserver import Authorizer",
				"{{.WrapperVarName}}.set_authorize_func(lambda metadata, full_method, req=None, _authorizer=authorizer: _authorizer.authorize(metadata, full_method, req))",
				"{{.WrapperVarName}}.set_authenticate_func(lambda metadata, target_namespace=None, _authorizer=authorizer: _authorizer.authenticate(metadata, target_namespace))",
				"if not {{.WrapperVarName}}.has_authorizer():",
			},
			message: "Python ServiceManager must wire metadata-aware auth callbacks before registering wrappers",
		},
		{
			name: "python_wrapper",
			path: "pkg/templates/python/types/servicewrapper.py.tmpl",
			want: []string{
				"def has_authorizer(self) -> bool:",
				"return self._authorize_func is not None and self._authenticate_func is not None",
				"def _metadata_from_context(self, context):",
				"self._authorize_func(self._metadata_from_context(context), full_method, req)",
				"self._authenticate_func(self._metadata_from_context(context), target_namespace)",
			},
			message: "Python wrappers must expose startup auth status and pass gRPC metadata to auth callbacks",
		},
		{
			name: "typescript",
			path: "pkg/templates/typescript/types/service_manager.ts.tmpl",
			want: []string{
				"{{.WrapperVarName}}.setAuthorizeFunc((fullMethod, metadata, req) => authorizer.authorize(metadata, fullMethod, req));",
				"{{.WrapperVarName}}.setAuthenticateFunc((metadata, targetNamespace) => authorizer.authenticate(metadata, targetNamespace));",
				"if (!{{.WrapperVarName}}.hasAuthorizer()) {",
			},
			message: "TypeScript ServiceManager must assert mandatory auth wiring before serving",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := readTemplateForAuditTest(t, root, tt.path)
			for _, want := range tt.want {
				if !strings.Contains(src, want) {
					t.Fatalf("%s: missing %q in %s:\n%s", tt.message, want, tt.path, src)
				}
			}
			for _, forbidden := range tt.forbid {
				if strings.Contains(src, forbidden) {
					t.Fatalf("%s: found forbidden %q in %s:\n%s", tt.message, forbidden, tt.path, src)
				}
			}
		})
	}
}

func TestRustKeyTemplateUsesNativeRsaKeySizeType(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	src := readTemplateForAuditTest(t, root, "pkg/templates/rust/chassis/key/src/keys.rs.tmpl")

	for _, want := range []string{
		"fn rsa_alg_for_key_size(key_bytes: usize) -> Result<&'static str>",
		"let key_bytes = (key.n().bits() + 7) / 8;",
		"let alg = rsa_alg_for_key_size(key_bytes)?;",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Rust key template missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "fn rsa_alg_for_key_size(key_bytes: u64)") {
		t.Fatalf("Rust key template regressed to u64 key_bytes helper:\n%s", src)
	}
}

func TestGeneratedSurfacesDoNotExposeValidationBypass(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	forbidden := []string{
		"DisableValidation",
		"disableValidation",
		"disable_validation",
		"SetDisableValidation",
		"setDisableValidation",
		"set_disable_validation",
		"isValidationDisabled",
		"is_validation_disabled",
		"disable_validation_method",
	}
	scanRoots := []string{
		"pkg/internal",
		"pkg/proto",
		"pkg/templates",
	}

	for _, scanRoot := range scanRoots {
		absRoot := filepath.Join(root, scanRoot)
		if err := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				base := d.Name()
				if base == "target" || base == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".go" && ext != ".proto" && ext != ".tmpl" {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(b)
			for _, banned := range forbidden {
				if strings.Contains(src, banned) {
					rel, _ := filepath.Rel(root, path)
					t.Fatalf("%s must not contain validation bypass symbol %q", rel, banned)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("scan %s: %v", scanRoot, err)
		}
	}
}

func TestGeneratedAuthorizersPreserveAuthStatusCodes(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	tests := []struct {
		name   string
		path   string
		want   []string
		forbid []string
	}{
		{
			name: "java",
			path: "pkg/templates/java/chassis/grpcserver/Authorizer.java.tmpl",
			want: []string{
				`throw ProtoException.newErrUnauthorized(null,"missing authorization header");`,
				`throw ProtoException.newErrUnauthorized(null,"invalid token: " + e.getMessage());`,
				`throw ProtoException.newErrUnauthorized(null,"token verification failed: " + e.getMessage());`,
				`throw ProtoException.newErrPermissionDenied(null,`,
				`"token audience \"" + aud + "\" is not accepted by service \"" + SERVICE_NAME + "\"");`,
				`throw ProtoException.newErrPermissionDenied(null, "token missing service_entitlements claim");`,
			},
			forbid: []string{
				`throw ProtoException.newErrPermissionDenied(null,"missing authorization header");`,
				`throw ProtoException.newErrPermissionDenied(null, "missing authorization header");`,
				`throw ProtoException.newErrPermissionDenied(null,"invalid token: " + e.getMessage());`,
				`throw ProtoException.newErrPermissionDenied(null, "invalid token: " + e.getMessage());`,
				`throw ProtoException.newErrPermissionDenied(null,"token verification failed: " + e.getMessage());`,
				`throw ProtoException.newErrPermissionDenied(null, "token verification failed: " + e.getMessage());`,
				`throw ProtoException.newErrUnauthorized(null,
                    "token audience \"" + aud + "\" is not accepted by service \"" + SERVICE_NAME + "\"");`,
			},
		},
		{
			name: "python",
			path: "pkg/templates/python/chassis/grpcserver/authorizer.py.tmpl",
			want: []string{
				"from {{.PythonPackageBase}}.types.sys.errors.exceptions import permission_denied, unauthenticated",
				`raise unauthenticated("missing authorization header")`,
				`raise unauthenticated(f"invalid token: {e}")`,
				`raise unauthenticated(f"token verification failed: {e}")`,
				`raise permission_denied(`,
				`f'token audience "{aud}" is not accepted by service "{self._SERVICE_NAME}"'`,
				`raise permission_denied("token missing service_entitlements claim")`,
			},
			forbid: []string{
				`raise permission_denied("missing authorization header")`,
				`raise permission_denied(f"invalid token: {e}")`,
				`raise permission_denied(f"token verification failed: {e}")`,
				`raise unauthenticated(
                f'token audience "{aud}" is not accepted by service "{self._SERVICE_NAME}"'
            )`,
			},
		},
		{
			name: "typescript",
			path: "pkg/templates/typescript/chassis/grpcserver/Authorizer.ts.tmpl",
			want: []string{
				"export class AuthError extends Error {",
				"return new AuthError(grpc.status.UNAUTHENTICATED, message);",
				"return new AuthError(grpc.status.PERMISSION_DENIED, message);",
				"throw AuthError.unauthenticated('missing authorization header');",
				"throw AuthError.permissionDenied(`token audience \"${aud}\" is not accepted by service \"${Authorizer.SERVICE_NAME}\"`);",
				"throw AuthError.permissionDenied('token missing service_entitlements claim');",
			},
			forbid: []string{
				"throw new Error('missing authorization header');",
				"throw new Error('token missing service_entitlements claim');",
				"throw AuthError.unauthenticated(`token audience \"${aud}\" is not accepted by service \"${Authorizer.SERVICE_NAME}\"`);",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := readTemplateForAuditTest(t, root, tt.path)
			for _, want := range tt.want {
				if !strings.Contains(src, want) {
					t.Fatalf("%s missing %q:\n%s", tt.path, want, src)
				}
			}
			for _, forbidden := range tt.forbid {
				if strings.Contains(src, forbidden) {
					t.Fatalf("%s contains stale auth status mapping %q:\n%s", tt.path, forbidden, src)
				}
			}
		})
	}
}

func readTemplateForAuditTest(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read template %s: %v", rel, err)
	}
	return string(b)
}

// A trait's audience is the trait name, not the name of the host implementing
// it (query-trait ruling Q2), so a host accepts `aud` iff it is the host's own
// label OR the name of a trait it hosts. Before this, every generated Authorizer
// compared `aud` against a single baked-in service name and rejected the trait
// audience outright — stratum answered a correctly-authenticated `aud=query`
// call with `token audience "query" does not match service "stratum"`.
//
// This pins the membership form in every language: the accepted set must come
// from .AcceptedAudiences, and no authorizer may go back to testing `aud`
// against the lone service-name constant.
func TestGeneratedAuthorizersAcceptTraitAudiences(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	tests := []struct {
		name   string
		path   string
		want   []string
		forbid []string
	}{
		{
			name: "go",
			path: "pkg/templates/go/chassis/grpcserver/authorizer.go.tmpl",
			want: []string{
				"var acceptedAudiences = map[string]struct{}{",
				"{{- range .AcceptedAudiences}}",
				"if _, ok := acceptedAudiences[tw.GetAudience()]; !ok {",
			},
			forbid: []string{"tw.GetAudience() != serviceName"},
		},
		{
			name: "java",
			path: "pkg/templates/java/chassis/grpcserver/Authorizer.java.tmpl",
			want: []string{
				"static final java.util.List<String> ACCEPTED_AUDIENCES = java.util.List.of({{range $i, $a := .AcceptedAudiences}}",
				"if (!ACCEPTED_AUDIENCES.contains(aud)) {",
			},
			forbid: []string{"if (!SERVICE_NAME.equals(aud)) {"},
		},
		{
			name: "python",
			path: "pkg/templates/python/chassis/grpcserver/authorizer.py.tmpl",
			want: []string{
				`_ACCEPTED_AUDIENCES = frozenset([{{range $i, $a := .AcceptedAudiences}}`,
				"if aud not in self._ACCEPTED_AUDIENCES:",
			},
			forbid: []string{"if aud != self._SERVICE_NAME:"},
		},
		{
			name: "typescript",
			path: "pkg/templates/typescript/chassis/grpcserver/Authorizer.ts.tmpl",
			want: []string{
				"private static readonly ACCEPTED_AUDIENCES: ReadonlySet<string> = new Set([{{range $i, $a := .AcceptedAudiences}}",
				"if (!Authorizer.ACCEPTED_AUDIENCES.has(aud)) {",
			},
			forbid: []string{"if (aud !== Authorizer.SERVICE_NAME) {"},
		},
		{
			name: "rust",
			path: "pkg/templates/rust/chassis/grpcserver/src/authorizer.rs.tmpl",
			want: []string{
				`const ACCEPTED_AUDIENCES: &[&str] = &[{{range $i, $a := .AcceptedAudiences}}`,
				"if !ACCEPTED_AUDIENCES.contains(&aud) {",
			},
			forbid: []string{"if aud != SERVICE_NAME {"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := readTemplateForAuditTest(t, root, tt.path)
			for _, want := range tt.want {
				if !strings.Contains(src, want) {
					t.Fatalf("%s must accept the trait audience set; missing %q", tt.path, want)
				}
			}
			for _, banned := range tt.forbid {
				if strings.Contains(src, banned) {
					t.Fatalf("%s must not match aud against a single service name; found %q", tt.path, banned)
				}
			}
		})
	}
}
