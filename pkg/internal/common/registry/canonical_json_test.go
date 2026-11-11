package registry

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
	"google.golang.org/protobuf/encoding/protojson"
)

// registryGoldenFixture is a fixed PackageNode for the registry.json golden
// test. It uses only fields every supported release line has, so the same
// fixture and golden file prove every line writes the same bytes (#307).
func registryGoldenFixture() *spec.PackageNode {
	return &spec.PackageNode{
		Name: "types",
		Children: []*spec.PackageNode{{
			Name:     "accounting_method",
			FullPath: "generated/accounting_method",
			Types: []*spec.TypeInfo{{
				Fqn:         "accounting_method.AccountingMethod",
				Name:        "AccountingMethod",
				Package:     "accounting_method",
				Description: "Quotes \"inside\", a key-like \"k\": \"v\", a tab\t, <html> & \\ backslash, é, \u2028.",
				SchemaJson:  "{\n  \"name\": \"AccountingMethod\",\n  \"fields\": []\n}",
			}},
		}, {
			Name:     "empty",
			FullPath: "generated/empty",
		}},
		Build: &spec.BuildInfo{TacplusVersion: "1.2.3"},
		Docs: map[string]string{
			"b.md": "# B\n",
			"a.md": "",
		},
	}
}

// TestRegistryJSONGolden pins registry.json's bytes for a fixed input. protojson
// adds a random extra space after each ':' seeded from the running binary's
// hash, so before #307 the same input gave different bytes from one release
// (and one architecture) to the next. The golden file is the canonical format
// (2.5.8's: two spaces after ':', one after "$schema":), the same on every line.
func TestRegistryJSONGolden(t *testing.T) {
	got, err := marshalRegistryJSON(registryGoldenFixture())
	if err != nil {
		t.Fatalf("marshalRegistryJSON: %v", err)
	}
	golden := filepath.Join("testdata", "registry.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("registry.json differs from %s:\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

// TestCanonicalJSONIgnoresInputLayout: the formatter's output depends only on
// the JSON value, never on the whitespace protojson (or anything else) chose.
func TestCanonicalJSONIgnoresInputLayout(t *testing.T) {
	node := registryGoldenFixture()
	multiline, err := protojson.MarshalOptions{Multiline: true, Indent: "  ", UseProtoNames: true}.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	// Both of protojson's layouts, one space after ':' and two, and others.
	var oneSpace, tabbed bytes.Buffer
	if err := json.Indent(&oneSpace, compact, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := json.Indent(&tabbed, compact, "", "\t"); err != nil {
		t.Fatal(err)
	}
	twoSpace := regexp.MustCompile(`(?m)^(\s*"[^"\\]*"): `).ReplaceAll(oneSpace.Bytes(), []byte("$1:  "))
	if bytes.Equal(twoSpace, oneSpace.Bytes()) {
		t.Fatal("fixture: no key rewritten to two spaces")
	}

	want, err := canonicalJSON(multiline)
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string][]byte{"compact": compact, "one space": oneSpace.Bytes(), "two spaces": twoSpace, "tabs": tabbed.Bytes()} {
		got, err := canonicalJSON(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: output differs:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}
}

func TestCanonicalJSONLayout(t *testing.T) {
	in := ` { "a" : [ 1 , { } , [ ] , "x: \"y\", {z}" ] , "b" :{"c":null,"d":true} } `
	want := strings.Join([]string{
		`{`,
		`  "a":  [`,
		`    1,`,
		`    {},`,
		`    [],`,
		`    "x: \"y\", {z}"`,
		`  ],`,
		`  "b":  {`,
		`    "c":  null,`,
		`    "d":  true`,
		`  }`,
		`}`,
	}, "\n")
	got, err := canonicalJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCanonicalJSONRejectsInvalid(t *testing.T) {
	if _, err := canonicalJSON([]byte(`{"a":`)); err == nil {
		t.Fatal("want an error for invalid JSON")
	}
}
