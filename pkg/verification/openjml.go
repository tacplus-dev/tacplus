// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package verification

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// OpenJML ESC proves the JML specs the Java generator writes on each strict
// validator (#257): every `ensures` holds whenever the validator returns
// normally, for every value the message's getters can return. The validators
// run on protobuf-java, which ESC does not read: spec stubs (.jml) model each
// message the validators take as an immutable value whose getters are pure and
// return a fixed, arbitrary value (a ghost field each), which is the property
// wanted. Each validator with specs is one harness of the report.

// openjmlProver is the SMT solver ESC runs: the z3 4.7.1 the OpenJML release
// bundles.
const openjmlProver = "z3_4_7"

// jmlSpecLineRe matches a JML method-spec line the generator writes.
var jmlSpecLineRe = regexp.MustCompile(`^\s*//@\s*(requires|ensures)\b(.*?);?\s*$`)

// javaMethodRe matches a generated validator declaration: its name and the
// type of its first parameter (the message).
var javaMethodRe = regexp.MustCompile(`^\s*public\s+static\s+\w+\s+(\w+)\s*\(\s*([\w.]+)\s+\w+`)

// javaPackageRe and protoExceptionImportRe read a source's package and the
// ProtoException it throws.
var (
	javaPackageRe          = regexp.MustCompile(`(?m)^package\s+([\w.]+)\s*;`)
	protoExceptionImportRe = regexp.MustCompile(`(?m)^import\s+([\w.]+\.ProtoException)\s*;`)
)

// openjmlCompletedRe matches OpenJML's per-method verdict (--progress).
// Example: "Completed proof of p.Helpers.validateBoundsDepth(p.Kitchensink.Bounds,int) with prover z3_4_7 - no warnings [12.3 secs]"
var openjmlCompletedRe = regexp.MustCompile(`^Completed proof of ([\w.$]+)\.(\w+)\(([^)]*)\) with prover \S+ - (.+?)(?: \[[\d.]+ secs\])?$`)

// openjmlMethodRe extracts the method a `verify:` finding is in.
var openjmlMethodRe = regexp.MustCompile(`\bin method (\w+)|\bmethod [\w.$]+\.(\w+)\b`)

// jmlMethod is one validator carrying JML specs.
type jmlMethod struct {
	Name     string   // validateBoundsDepth
	Param    string   // Kitchensink.Bounds, as written in the source
	Clauses  []string // "ensures msg.getPort() > 0", ...
	FileLine int
}

// jmlSource is a Java source whose validators carry JML specs.
type jmlSource struct {
	Path           string
	Package        string
	ProtoException string // fully qualified, "" if the source imports none
	Methods        []jmlMethod
}

// runOpenJml runs OpenJML ESC on every generated Java source with JML specs
// under outputDir, grouped by the Maven module that compiles it.
func runOpenJml(outputDir string) (*spec.VerificationReport, error) {
	zap.L().Info("searching for JML specs", zap.String("dir", outputDir))
	sources, err := findJmlSources(outputDir)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		zap.L().Info("no Java source with JML specs found")
		return nil, nil
	}
	byPath := make(map[string]jmlSource, len(sources))
	var paths []string
	for _, s := range sources {
		byPath[s.Path] = s
		paths = append(paths, s.Path)
	}

	modules := groupByMavenModule(paths)
	if len(modules) == 0 {
		return nil, fmt.Errorf("no Maven project (pom.xml) found for any Java source with JML specs: run tacplus verify in the Maven module that compiles the generated code")
	}

	report := &spec.VerificationReport{
		Tool:        "openjml",
		ToolVersion: toolVersion("openjml", "-version"),
		Language:    "java",
	}

	moduleDirs := make([]string, 0, len(modules))
	for dir := range modules {
		moduleDirs = append(moduleDirs, dir)
	}
	sort.Strings(moduleDirs)
	for _, moduleDir := range moduleDirs {
		zap.L().Info("compiling maven module", zap.String("module", moduleDir))
		if err := mavenCompile(moduleDir); err != nil {
			return nil, err
		}
		depCP, err := mavenDepClasspath(moduleDir)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve classpath for %s: %w", filepath.Base(moduleDir), err)
		}
		classpath := filepath.Join(moduleDir, "target", "classes") + ":" + depCP

		files := modules[moduleDir]
		sort.Strings(files)
		for _, path := range files {
			src := byPath[path]
			specsDir := filepath.Join(moduleDir, "target", "openjml-specs")
			if err := writeJmlStubs(specsDir, classpath, src); err != nil {
				return nil, fmt.Errorf("spec stubs for %s: %w", path, err)
			}
			zap.L().Info("running OpenJML ESC", zap.String("file", path), zap.Int("methods", len(src.Methods)))
			output := runOpenJmlEsc(path, classpath, specsDir, src.Methods)
			for _, h := range parseOpenJmlOutput(output, src) {
				zap.L().Info("harness", zap.String("name", h.HarnessName), zap.String("status", h.Status), zap.String("detail", h.Detail))
				report.Harnesses = append(report.Harnesses, h)
			}
		}
	}

	for _, h := range report.Harnesses {
		report.Total++
		if h.Status == "verified" {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	return report, nil
}

// findJmlSources walks dir for Java sources whose validators carry JML specs.
func findJmlSources(dir string) ([]jmlSource, error) {
	var sources []jmlSource
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case "target", ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".java") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if src, ok := parseJmlSource(path, string(data)); ok {
			sources = append(sources, src)
		}
		return nil
	})
	return sources, err
}

// parseJmlSource reads the validators of a Java source that carry JML specs:
// the //@ requires/ensures lines directly above a method declaration.
func parseJmlSource(path, text string) (jmlSource, bool) {
	src := jmlSource{Path: path}
	if m := javaPackageRe.FindStringSubmatch(text); m != nil {
		src.Package = m[1]
	}
	if m := protoExceptionImportRe.FindStringSubmatch(text); m != nil {
		src.ProtoException = m[1]
	}
	var clauses []string
	for i, line := range strings.Split(text, "\n") {
		if m := jmlSpecLineRe.FindStringSubmatch(line); m != nil {
			clauses = append(clauses, m[1]+strings.TrimRight(m[2], ";"))
			continue
		}
		if len(clauses) > 0 {
			if m := javaMethodRe.FindStringSubmatch(line); m != nil {
				src.Methods = append(src.Methods, jmlMethod{Name: m[1], Param: m[2], Clauses: clauses, FileLine: i + 1})
			}
		}
		clauses = nil
	}
	return src, len(src.Methods) > 0
}

// runOpenJmlEsc runs OpenJML ESC on the given methods of one source and returns
// its output.
func runOpenJmlEsc(path, classpath, specsDir string, methods []jmlMethod) string {
	names := make([]string, 0, len(methods))
	for _, m := range methods {
		names = append(names, m.Name)
	}
	args := []string{
		"--esc",
		"--progress",
		"-nowarn",
		// No --timeout: OpenJML 21-0.23 hands z3 a limit far below the seconds
		// asked for (600 gave up on a 25-second proof as UNKNOWN in 14).
		"--prover", openjmlProver,
		"--specs-path", specsDir,
		"-cp", classpath,
		"--method", strings.Join(names, ","),
		path,
	}
	cmd := exec.Command("openjml", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// A failed proof exits non-zero too; the verdicts are in the output.
		zap.L().Debug("openjml exited non-zero", zap.Error(err))
	}
	return string(output)
}

// parseOpenJmlOutput turns OpenJML's output into one harness per method of src:
// verified when ESC completed its proof with no warnings, failed otherwise (a
// finding, an unknown or timed-out proof, or a source that did not typecheck).
func parseOpenJmlOutput(output string, src jmlSource) []*spec.VerificationHarness {
	verdicts := make(map[string]string)
	findings := make(map[string][]string)
	var errorsOut []string

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if m := openjmlCompletedRe.FindStringSubmatch(line); m != nil {
			verdicts[m[2]] = m[4]
			continue
		}
		if strings.Contains(line, ": verify: ") {
			if m := openjmlMethodRe.FindStringSubmatch(line); m != nil {
				name := m[1]
				if name == "" {
					name = m[2]
				}
				findings[name] = append(findings[name], line)
			}
			continue
		}
		if strings.Contains(line, ": error: ") || strings.HasPrefix(line, "error: ") {
			errorsOut = append(errorsOut, line)
		}
	}

	className := "Helpers"
	if base := strings.TrimSuffix(filepath.Base(src.Path), ".java"); base != "" {
		className = base
	}
	var harnesses []*spec.VerificationHarness
	for _, m := range src.Methods {
		h := &spec.VerificationHarness{
			HarnessName: src.Package + "." + className + "." + m.Name,
			Contracts:   m.Clauses,
		}
		verdict, proved := verdicts[m.Name]
		switch {
		case proved && verdict == "no warnings" && len(findings[m.Name]) == 0:
			h.Status = "verified"
		case proved:
			h.Status = "failed"
			h.Detail = "ESC: " + verdict
			if len(findings[m.Name]) > 0 {
				h.Detail += "\n" + strings.Join(findings[m.Name], "\n")
			}
		case len(errorsOut) > 0:
			h.Status = "failed"
			h.Detail = "OpenJML did not check the source:\n" + strings.Join(firstN(errorsOut, 20), "\n")
		default:
			h.Status = "failed"
			h.Detail = "OpenJML reported no proof of this method:\n" + lastLines(output, 20)
		}
		harnesses = append(harnesses, h)
	}
	return harnesses
}

// writeJmlStubs writes the spec stubs ESC reads in place of the classes the
// validators of src run on: each message class they take (its no-argument
// getters pure, each returning a ghost field), ProtoException (constructed
// without side effects) and ByteString (its size, a ghost field).
func writeJmlStubs(specsDir, classpath string, src jmlSource) error {
	// Group the message types by their top-level (outer) class.
	nested := make(map[string]map[string]bool) // outer FQN -> nested path ("Bounds", "A.B")
	for _, m := range src.Methods {
		parts := strings.Split(m.Param, ".")
		if len(parts) < 2 {
			continue
		}
		outer := parts[0]
		if src.Package != "" {
			outer = src.Package + "." + outer
		}
		if nested[outer] == nil {
			nested[outer] = make(map[string]bool)
		}
		nested[outer][strings.Join(parts[1:], ".")] = true
	}

	for outer, paths := range nested {
		stub, err := messageStub(classpath, outer, paths)
		if err != nil {
			return err
		}
		if err := writeStub(specsDir, outer, stub); err != nil {
			return err
		}
	}
	if src.ProtoException != "" {
		stub, err := classStub(classpath, src.ProtoException, func(sig javapMember, _ *typeNamer) (string, bool) {
			if sig.Constructor && sig.Params == "int, java.lang.String" {
				return "//@ public normal_behavior\n//@   ensures true;\n" + sig.declare("/*@ pure @*/", "int code, java.lang.String message"), true
			}
			return "", false
		})
		if err != nil {
			return err
		}
		if err := writeStub(specsDir, src.ProtoException, stub); err != nil {
			return err
		}
	}
	stub, err := classStub(classpath, "com.google.protobuf.ByteString", func(sig javapMember, names *typeNamer) (string, bool) {
		if sig.Name == "size" && sig.Params == "" && !sig.Static {
			return ghostGetter(sig, names), true
		}
		return "", false
	})
	if err != nil {
		return err
	}
	return writeStub(specsDir, "com.google.protobuf.ByteString", stub)
}

// messageStub renders the .jml of a protoc outer class with the message
// classes at the given nested paths.
func messageStub(classpath, outer string, paths map[string]bool) (string, error) {
	header, _, err := javapClass(classpath, outer)
	if err != nil {
		return "", err
	}
	names := newTypeNamer(packageOf(outer))
	// Build the tree of nested classes to declare.
	type node struct {
		children map[string]*node
		order    []string
	}
	root := &node{children: map[string]*node{}}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	for _, p := range sorted {
		n := root
		for _, part := range strings.Split(p, ".") {
			child, ok := n.children[part]
			if !ok {
				child = &node{children: map[string]*node{}}
				n.children[part] = child
				n.order = append(n.order, part)
			}
			n = child
		}
	}

	var b strings.Builder
	var render func(n *node, binary string) error
	render = func(n *node, binary string) error {
		for _, name := range n.order {
			childBinary := binary + "$" + name
			childHeader, members, err := javapClass(classpath, childBinary)
			if err != nil {
				return err
			}
			b.WriteString(nestedHeader(childHeader) + " {\n")
			seen := map[string]bool{}
			for _, sig := range members {
				if sig.Static || sig.Constructor || sig.Params != "" || seen[sig.Name] || sig.Return == "void" {
					continue
				}
				if !strings.HasPrefix(sig.Name, "get") && !strings.HasPrefix(sig.Name, "has") {
					continue
				}
				seen[sig.Name] = true
				b.WriteString(ghostGetter(sig, names))
			}
			if err := render(n.children[name], childBinary); err != nil {
				return err
			}
			b.WriteString("}\n")
		}
		return nil
	}
	b.WriteString(header + " {\n")
	if err := render(root, outer); err != nil {
		return "", err
	}
	b.WriteString("}\n")
	return packageLine(outer) + names.importLines() + b.String(), nil
}

// classStub renders the .jml of a top-level class: its declaration and the
// members pick keeps.
func classStub(classpath, fqn string, pick func(javapMember, *typeNamer) (string, bool)) (string, error) {
	header, members, err := javapClass(classpath, fqn)
	if err != nil {
		return "", err
	}
	names := newTypeNamer(packageOf(fqn))
	var b strings.Builder
	b.WriteString(header + " {\n")
	for _, sig := range members {
		if decl, ok := pick(sig, names); ok {
			b.WriteString(decl)
		}
	}
	b.WriteString("}\n")
	return packageLine(fqn) + names.importLines() + b.String(), nil
}

// ghostGetter declares a pure no-argument method whose result is a ghost field
// of the object: a fixed, arbitrary value per object, as on an immutable one.
// The field is final: a call ESC has no spec for (a lambda, a Validation
// helper) may assign every field, and would otherwise unfix the getters. A
// getter whose type cannot be named short (see typeNamer) is left out.
func ghostGetter(sig javapMember, names *typeNamer) string {
	ghost := "_" + sig.Name
	typ := names.short(sig.Return)
	if qualifiedNameRe.MatchString(typ) {
		return ""
	}
	sig.Return = typ
	return fmt.Sprintf("//@ public final ghost %s %s;\n//@ public normal_behavior\n//@   ensures \\result == %s;\n%s",
		typ, ghost, ghost, sig.declare("/*@ pure @*/", ""))
}

// qualifiedNameRe matches a package-qualified Java type name.
var qualifiedNameRe = regexp.MustCompile(`\b[a-z_][\w$]*(?:\.[\w$]+)+`)

// typeNamer names types in a stub by their top-level class, imported: a
// package-qualified name cannot go in a JML comment when a segment is a JML
// keyword, as `model` is (tacplus's default output-dir). A simple name already
// taken by another package's class stays qualified.
type typeNamer struct {
	pkg     string
	imports map[string]string // simple top-level name -> its package (java.lang and pkg need no import)
}

func newTypeNamer(pkg string) *typeNamer {
	return &typeNamer{pkg: pkg, imports: map[string]string{}}
}

// short rewrites each qualified name in t (a type, generic arguments included).
func (n *typeNamer) short(t string) string {
	return qualifiedNameRe.ReplaceAllStringFunc(t, func(name string) string {
		parts := strings.Split(name, ".")
		top := -1
		for i, p := range parts {
			if p != "" && p[0] >= 'A' && p[0] <= 'Z' {
				top = i
				break
			}
		}
		if top <= 0 {
			return name
		}
		pkg, simple := strings.Join(parts[:top], "."), parts[top]
		if owner, taken := n.imports[simple]; taken && owner != pkg {
			return name
		}
		n.imports[simple] = pkg
		return strings.Join(parts[top:], ".")
	})
}

// importLines are the import declarations short's names need, sorted.
func (n *typeNamer) importLines() string {
	var lines []string
	for simple, pkg := range n.imports {
		if pkg != "java.lang" && pkg != n.pkg {
			lines = append(lines, "import "+pkg+"."+simple+";\n")
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "") + "\n"
}

// writeStub writes a .jml for the top-level class fqn under specsDir.
func writeStub(specsDir, fqn, stub string) error {
	path := filepath.Join(specsDir, filepath.FromSlash(strings.ReplaceAll(fqn, ".", "/"))+".jml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(stub), 0o644)
}

// packageLine is the package declaration of a fully qualified class name.
func packageLine(fqn string) string {
	if pkg := packageOf(fqn); pkg != "" {
		return "package " + pkg + ";\n\n"
	}
	return ""
}

// packageOf is the package of a fully qualified top-level class name.
func packageOf(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i > 0 {
		return fqn[:i]
	}
	return ""
}

// nestedHeader turns javap's header of a member class ("public final class
// p.Outer$Msg extends ...") into its declaration inside the outer class.
func nestedHeader(header string) string {
	return strings.Replace(header, "public final class ", "public static final class ", 1)
}

// javapMember is one public member javap lists.
type javapMember struct {
	Modifiers   []string // public, abstract, final, ...
	Static      bool
	Constructor bool
	Return      string // "" for a constructor
	Name        string
	Params      string // as javap prints them, "" for none
}

// declare renders the member's declaration with an extra modifier comment and
// parameter list.
func (m javapMember) declare(jml, params string) string {
	var mods []string
	for _, mod := range m.Modifiers {
		switch mod {
		case "native", "synchronized", "default":
			continue
		}
		mods = append(mods, mod)
	}
	mods = append(mods, jml)
	if m.Constructor {
		return fmt.Sprintf("%s %s(%s);\n", strings.Join(mods, " "), m.Name, params)
	}
	return fmt.Sprintf("%s %s %s(%s);\n", strings.Join(mods, " "), m.Return, m.Name, params)
}

// javapMemberRe matches a method or constructor javap lists: modifiers, return
// type (none for a constructor, whose name javap prints qualified), name and
// parameters.
var javapMemberRe = regexp.MustCompile(`^((?:(?:public|protected|private|static|final|abstract|native|synchronized|default|strictfp)\s+)*)(?:([\w$.]+(?:<.*>)?(?:\[\])*)\s+)?([\w$.]+)\((.*)\)(?:\s+throws\s+.*)?;$`)

// javapClass lists a class's declaration (its binary names dotted) and public
// methods and constructors.
func javapClass(classpath, binaryName string) (string, []javapMember, error) {
	out, err := exec.Command("javap", "-public", "-cp", classpath, binaryName).CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("javap %s: %w\n%s", binaryName, err, string(out))
	}
	return parseJavap(string(out), binaryName)
}

// parseJavap parses `javap -public` output; see javapClass.
func parseJavap(out, binaryName string) (string, []javapMember, error) {
	dotted := func(s string) string { return strings.ReplaceAll(s, "$", ".") }
	simple := binaryName
	if i := strings.LastIndexAny(simple, ".$"); i >= 0 {
		simple = simple[i+1:]
	}
	var header string
	var members []javapMember
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Compiled from") || line == "" || line == "}" {
			continue
		}
		if header == "" && strings.HasSuffix(line, "{") {
			h := strings.TrimSpace(strings.TrimSuffix(line, "{"))
			// The class's own name, simple, in its declaration.
			h = strings.Replace(h, " "+binaryName, " "+simple, 1)
			header = dotted(h)
			continue
		}
		m := javapMemberRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mods := strings.Fields(m[1])
		member := javapMember{Modifiers: mods, Return: dotted(m[2]), Name: m[3], Params: dotted(m[4])}
		for _, mod := range mods {
			if mod == "static" {
				member.Static = true
			}
		}
		if m[2] == "" {
			member.Constructor = true
			member.Name = simple
			member.Return = ""
		}
		members = append(members, member)
	}
	if header == "" {
		return "", nil, fmt.Errorf("javap %s: no class declaration in output", binaryName)
	}
	return header, members, nil
}

func firstN(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:n]
	}
	return lines
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
