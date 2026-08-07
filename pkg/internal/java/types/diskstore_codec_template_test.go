// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

func TestJavaDiskstoreCodecDoesNotRequireCommonsCompressAtCompileTime(t *testing.T) {
	src, err := templates.FS.ReadFile("java/chassis/diskstore/Codec.java.tmpl")
	if err != nil {
		t.Fatalf("read Codec.java template: %v", err)
	}
	rendered := string(src)

	if strings.Contains(rendered, "import org.apache.commons.compress") {
		t.Fatalf("Java diskstore codec imports commons-compress directly:\n%s", rendered)
	}
	for _, want := range []string{
		"Class.forName(className)",
		"\"org.apache.commons.compress.compressors.bzip2.BZip2CompressorOutputStream\"",
		"\"org.apache.commons.compress.compressors.bzip2.BZip2CompressorInputStream\"",
		"BZip2 codec requires runtime Maven dependency ",
		"org.apache.commons:commons-compress",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java diskstore codec template missing %q:\n%s", want, rendered)
		}
	}
}
