// Copyright (c) 2026 Second Star Cavalier LLC

package chassis

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	rustLoggerTmpl = "../../../templates/rust/chassis/logger/src/logger.rs.tmpl"
	rustConfigTmpl = "../../../templates/rust/chassis/logger/src/config.rs.tmpl"
	rustLoaderTmpl = "../../../templates/rust/chassis/grpcconfig/src/loader.rs.tmpl"
)

// add_directive("h2=warn".parse().unwrap()) and friends. Only transport pins are
// of interest -- the level directive is built from a variable and does not match.
var pinRe = regexp.MustCompile(`add_directive\("([a-z0-9_:]+)=warn"`)

func readTmpl(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// pins returns the set of crates pinned to WARN in the production code of a
// template. The #[cfg(test)] module is cut first: logger.rs's own pin test builds
// an EnvFilter inline and asserts on it, so its directives would otherwise be
// counted as if install() had them.
func pins(s string) []string {
	if i := strings.Index(s, "#[cfg(test)]"); i >= 0 {
		s = s[:i]
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range pinRe.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Rust has TWO logger install paths -- Logger::install (hardcoded defaults) and
// install_from_config (the typed logger.LoggerConfig). Both set tracing's
// process-wide subscriber, and only one of them runs. Transport-layer crates
// (h2, hyper, tonic) emit raw HTTP/2 frames including authorization headers with
// live service JWTs at DEBUG/TRACE, so each install must pin them to WARN.
//
// A pin present in only one path is a live credential leak on whichever path
// runs. install_from_config shipped without ANY pins while nothing called it;
// the moment grpcconfig's load_config started calling it, that omission would
// have become a real leak. Assert the two sets are identical rather than
// checking either one in isolation.
func TestRustLoggerTransportPinsMatchAcrossInstallPaths(t *testing.T) {
	defaultPins := pins(readTmpl(t, rustLoggerTmpl))
	configPins := pins(readTmpl(t, rustConfigTmpl))

	if len(defaultPins) == 0 {
		t.Fatal("logger.rs.tmpl Logger::install has no transport pins at all")
	}
	if len(configPins) == 0 {
		t.Fatal("config.rs.tmpl install_from_config has no transport pins -- " +
			"credential-bearing HTTP/2 frames are loggable on the typed-config path")
	}
	if strings.Join(defaultPins, ",") != strings.Join(configPins, ",") {
		t.Errorf("transport pins drifted between the two rust logger install paths\n"+
			"  Logger::install      (logger.rs): %v\n"+
			"  install_from_config  (config.rs): %v\n"+
			"both paths set the global subscriber; a crate pinned in only one leaks "+
			"authorization headers whenever the other path runs", defaultPins, configPins)
	}
}

// The typed config must actually be applied. install_from_config existed and was
// correct for a long time while nothing called it, so grpcconfig's load_config
// used hardcoded defaults and every field of logger.LoggerConfig -- format,
// level, output paths, initial fields -- was inert in rust.
//
// Ordering is load-bearing: both installs call mark_installed() and the first
// one wins, so an unconditional default install at the top of load_config makes
// the typed install unreachable no matter where it appears later.
func TestRustLoaderInstallsFromTypedConfig(t *testing.T) {
	s := readTmpl(t, rustLoaderTmpl)

	if !strings.Contains(s, "logger::install_from_config(&cfg.logger") {
		t.Error("loader.rs.tmpl must install the logger from the typed config " +
			"(logger::install_from_config(&cfg.logger...)); otherwise grpcconfig " +
			"field 9 (logger.LoggerConfig) is generated but never applied")
	}

	// The default install is still the correct fallback when config load fails,
	// but it must be on the error path only.
	idxDefault := strings.Index(s, "logger::Logger::default().install()")
	idxTyped := strings.Index(s, "logger::install_from_config")
	if idxDefault >= 0 && idxTyped >= 0 && idxDefault < idxTyped {
		t.Error("loader.rs.tmpl installs the DEFAULT logger before the typed one; " +
			"the first install wins (mark_installed) so the typed config would be " +
			"silently ignored. The default install belongs on the load-failure path.")
	}
}
