// Copyright (c) 2026 Second Star Cavalier LLC

package logging

import (
	"os"
	"strings"
	"testing"
)

func readTmpl(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// Every language ships a full logger library driven by logger.LoggerConfig
// (grpcconfig field 9), and for most of them nothing ever called it: the config
// parsed cleanly, validated cleanly, and was then discarded while the process
// logged with hardcoded defaults. That is the same "generated capability, no call
// site" shape that hid the credential redactor and the health probe defaults, and
// it is invisible to any test that only checks the library exists.
//
// Pin the call site in each language's config loader -- the one place every
// service reaches, including those that never construct a ServiceManager.
//
// This is a TEMPLATE-level assertion, and for java it is defence in depth on top
// of a compile gate. For python and typescript it is the ONLY gate: the
// determinism and current-generation fixtures emit neither chassis/grpcconfig nor
// chassis/logger for those two languages, so no gate generates those files, and
// nothing anywhere compiles them. Treat a green result here as evidence that the
// call site is present, NOT that the generated code builds.
func TestConfigLoadersApplyTypedLoggerConfig(t *testing.T) {
	cases := []struct {
		lang string
		path string
		want string
	}{
		{
			lang: "java",
			path: "../../../templates/java/chassis/grpcconfig/GrpcConfigLoader.java.tmpl",
			want: "LogbackConfigurator.install(cfg.getLogger());",
		},
		{
			lang: "python",
			path: "../../../templates/python/chassis/grpcconfig/loader.py.tmpl",
			want: "LoggerConfigurator.install(cfg.logger)",
		},
		{
			lang: "typescript",
			path: "../../../templates/typescript/chassis/grpcconfig/loader.ts.tmpl",
			want: "WinstonConfigurator.install(cfg.logger)",
		},
	}

	for _, c := range cases {
		s := readTmpl(t, c.path)
		if !strings.Contains(s, c.want) {
			t.Errorf("%s config loader does not apply the typed logger config: expected %q.\n"+
				"Without it, logger.LoggerConfig (grpcconfig field 9) is parsed and validated "+
				"and then ignored -- format, level, output paths and initial fields all inert.",
				c.lang, c.want)
		}
	}
}

// java's bootstrap install runs before the config is parsed so that credential
// redaction and the transport logger pins engage before any gRPC channel exists.
// The typed install must come AFTER, and both must be present: dropping the first
// reopens the credential-leak window during startup, and dropping the second
// leaves the typed config inert.
//
// Unlike rust, java's install() re-applies cleanly and always pins the transport
// loggers, so calling it twice is safe -- the ordering requirement here is about
// coverage, not about which call wins.
func TestJavaLoaderInstallsBootstrapThenTypedLogger(t *testing.T) {
	s := readTmpl(t, "../../../templates/java/chassis/grpcconfig/GrpcConfigLoader.java.tmpl")

	bootstrap := strings.Index(s, "LogbackConfigurator.install(null);")
	typed := strings.Index(s, "LogbackConfigurator.install(cfg.getLogger());")

	if bootstrap < 0 {
		t.Error("java loader lost its pre-parse LogbackConfigurator.install(null); " +
			"credential redaction must engage before any gRPC channel is created")
	}
	if typed < 0 {
		t.Fatal("java loader does not install the typed logger config")
	}
	if bootstrap >= 0 && typed < bootstrap {
		t.Error("java loader installs the typed logger config before the bootstrap " +
			"install; the bootstrap install exists to cover the window before the " +
			"config is parsed and must come first")
	}
}

// The JSON encoder must emit the throwable.
//
// It did not, and the loss was silent: logger.error(msg, e) is the ordinary way
// a Java service reports a failure, and the encoder wrote only the formatted
// message. Many JDK exceptions carry a bare message -- an AccessDeniedException's
// message is nothing but the path -- so a permission denial on eventbus's data
// volume printed as a naked filename with no type and no frames, and the service
// retried it 131 times before the cause could be named.
//
// The frame and cause-depth caps matter as much as the field: an unbounded trace
// on a hot error path is its own outage, and a self-referential cause chain would
// never terminate.
func TestJavaJsonEncoderEmitsThrowable(t *testing.T) {
	s := readTmpl(t, "../../../templates/java/chassis/logger/LogbackConfigurator.java.tmpl")

	if !strings.Contains(s, `appendField(out, "exception", stack);`) {
		t.Error("the JSON encoder does not emit an exception field; every stack trace " +
			"logged by a Java service is discarded at the output boundary")
	}
	if !strings.Contains(s, "renderThrowable(event.getThrowableProxy())") {
		t.Error("the encoder must read the throwable off the logging event")
	}
	if !strings.Contains(s, "MAX_FRAMES_PER_THROWABLE") || !strings.Contains(s, "MAX_CAUSE_DEPTH") {
		t.Error("throwable rendering must be bounded in both frames and cause depth")
	}
	if !strings.Contains(s, `"Caused by: "`) {
		t.Error("the cause chain must be rendered; the root cause is usually the useful frame")
	}
	// The whole line still goes through CredentialRedactor. A stack trace can carry
	// a secret in an exception message, so the redaction boundary must stay AFTER
	// the exception field is appended, not before it.
	exc := strings.Index(s, `appendField(out, "exception", stack);`)
	red := strings.Index(s, "CredentialRedactor.redact(out.toString())")
	if exc >= 0 && red >= 0 && red < exc {
		t.Error("credential redaction runs before the exception field is appended; " +
			"a secret in an exception message would escape unredacted")
	}
	// An MDC key must not be able to displace or forge the field.
	if !strings.Contains(s, `|| "exception".equals(key)`) {
		t.Error(`"exception" must be a reserved JSON field so an MDC entry cannot spoof it`)
	}
}
