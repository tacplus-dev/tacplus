# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Prints the content hash of each chassis crate's self-tests in a generated
# workspace (tacplus#843): everything that decides what the gate's cargo calls
# build and run for that crate, normalized so that two fixtures rendering the
# same source hash the same. Used by cross-test/lib/rust-selftest-once.sh; run
# with python3 -I.
#
#   rust_selftest_hash.py WORKSPACE CMD... [::: CMD...]
#
# Each CMD is a cargo call as the gate runs it in WORKSPACE (`[env VAR=VALUE
# ...] cargo test -p A -p B ... [args]`). It prints `CRATE HASH` for each
# package a CMD selects, except the workspace's root package (the fixture's
# own). A crate's hash covers:
#   - the toolchain (`rustc -vV`, `cargo -V`);
#   - each CMD that selects the crate, with its -p packages replaced by the
#     crate alone and each VALUE that names a file replaced by its contents;
#   - the crate's part of each such CMD's `cargo tree` (normal, build and dev
#     edges, with each package's resolved version and features: the lock and
#     the feature unification over everything the CMD selects), as its sorted
#     set of edges;
#   - every file of each local package in those trees, except the model types
#     crate, of which it covers what the other local packages reach. Its unit
#     is a second-level module (`sys::token`), or a top module when a path
#     names an item of the top module itself (`sys/mod.rs` re-exports its
#     children by glob). The units are those the packages name
#     (`<types>::sys::token::...`), closed over the `crate::`, `super::` and
#     `self::` paths of the units' files, plus the crate's other files, with
#     each mod.rs less the `pub mod` lines of the modules left out (each
#     project's own protos, and `generated`'s per-project entitlements). A path
#     it cannot follow (a grouped or glob use of the crate root, an alias,
#     `super::` past the root, #[path], or an include!, include_str! or
#     include_bytes! of a path outside the file's own directory) covers the
#     whole crate; one of the last two in another local package covers the
#     workspace. It assumes what the generator writes: a type's impls are in
#     its own module, so a module left out cannot change one reached.
# The one normalization: the model types crate's name (`<base>-model-types`,
# `<base>_model_types`), which is the fixture's, and the workspace's path.

import hashlib
import os
import re
import subprocess
import sys

TYPES_NAME = re.compile(r"\b([A-Za-z0-9]+(?:[-_][A-Za-z0-9]+)*)[-_]model[-_]types\b")
TREE_PATH = re.compile(r"\((/[^)]*)\)")
INCLUDE = re.compile(r"\binclude(?:_str|_bytes)?!\s*\(\s*(?:\"([^\"\\\\]*)\"\s*\))?")
PATH_ATTR = re.compile(r"#\s*\[\s*path\b")
MOD_DECL = re.compile(r"^\s*(?:pub(?:\([^)]*\))?\s+)?mod\s+(\w+)\s*;", re.M)


def run(cmd, cwd=None):
    return subprocess.run(cmd, cwd=cwd, check=True, capture_output=True, text=True).stdout


def files_under(root):
    out = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d != "target")
        for name in sorted(filenames):
            out.append(os.path.join(dirpath, name))
    return out


def reads_outside(text):
    """Whether a file names a path the hash may not cover: #[path], or an
    include!, include_str! or include_bytes! of anything but a literal path
    below the file's own directory (the generated types include their sibling
    <name>.serde.rs and <name>.tonic.rs)."""
    if PATH_ATTR.search(text):
        return True
    for m in INCLUDE.finditer(text):
        target = m.group(1)
        if target is None or target.startswith("/") or ".." in target.split("/"):
            return True
    return False


def read(path):
    with open(path, "rb") as f:
        return f.read()


class Hasher:
    def __init__(self, workspace, types_names):
        self.h = hashlib.sha256()
        self.workspace = workspace
        self.types_names = types_names

    def normalize(self, data):
        text = data.decode("utf-8", "surrogateescape")
        text = text.replace(self.workspace, "<workspace>")
        for name in self.types_names:
            text = re.sub(r"\b" + re.escape(name) + r"\b", "<model-types>", text)
        return text.encode("utf-8", "surrogateescape")

    def add(self, label, data):
        data = self.normalize(data)
        self.h.update(self.normalize(label.encode()) + b"\0" + str(len(data)).encode() + b"\0" + data)

    def add_file(self, path):
        self.add("file " + os.path.relpath(path, self.workspace), read(path))


def packages(argv):
    pkgs = []
    for i, arg in enumerate(argv):
        if arg in ("-p", "--package") and i + 1 < len(argv):
            pkgs.append(argv[i + 1])
        elif arg.startswith("--package="):
            pkgs.append(arg.split("=", 1)[1])
    return pkgs


def module_path(types_dir, path):
    """The module a file of the types crate is (an include!d file: its
    includer's depth, which is all super:: needs)."""
    parts = os.path.relpath(path, types_dir).split(os.sep)
    parts[-1] = parts[-1][:-3] if parts[-1].endswith(".rs") else parts[-1]
    if parts[-1] == "mod":
        parts.pop()
    return parts


def unit_of(types_dir, path):
    """The unit a module path is in: its second-level module (sys::token) when
    it names one, else its top module (an item of sys/mod.rs, which re-exports
    its children by glob: all of sys). None: the crate root."""
    if not path:
        return None
    if len(path) >= 2 and (os.path.isdir(os.path.join(types_dir, path[0], path[1]))
                           or os.path.isfile(os.path.join(types_dir, path[0], path[1] + ".rs"))):
        return tuple(path[:2])
    return (path[0],)


def unit_files(types_dir, unit):
    path = os.path.join(types_dir, *unit)
    files = files_under(path) if os.path.isdir(path) else []
    if os.path.isfile(path + ".rs"):
        files.append(path + ".rs")
    return files


def references(text, base, ident=None):
    """The module paths a file names, from its module path base: crate::,
    self:: and super:: paths, or ident:: (the types crate, from another
    package). None when one cannot be followed: a glob or group at the crate
    root, super:: past it, or ident bare (an alias)."""
    head = r"\b(?:crate|self|super)" if ident is None else r"\b" + re.escape(ident) + r"\b"
    out = []
    for m in re.finditer(head + r"((?:\s*::\s*\w+)*)(\s*::\s*[{*])?", text):
        word = re.match(r"\w+", m.group(0)).group(0)
        segs = re.findall(r"\w+", m.group(1))
        if ident is not None or word == "crate":
            path = []
        elif word == "self":
            path = list(base)
        else:
            path = list(base[:-1])
        if ident is None and not segs and not m.group(2):
            continue  # pub(crate), pub(super), a receiver, `self` in a use group
        for seg in segs:
            if seg == "super":
                if not path:
                    return None
                path.pop()
            elif seg != "self":
                path.append(seg)
        if not path and (m.group(2) or ident is not None):
            return None
        out.append(path)
    return out


def types_closure(types_dir, ident, other_files):
    """The units of the types crate the other packages reach, with their
    files, or None for the whole crate."""
    todo = []
    for path in other_files:
        if path.endswith(".rs"):
            refs = references(read(path).decode("utf-8", "surrogateescape"), [], ident)
            if refs is None:
                return None
            todo.extend(refs)
    reached, files = set(), []
    while todo:
        unit = unit_of(types_dir, todo.pop())
        if unit is None:
            return None
        if unit in reached or unit[:1] in reached:
            continue
        found = unit_files(types_dir, unit)
        if not found:
            return None
        reached.add(unit)
        for path in found:
            files.append(path)
            if not path.endswith(".rs"):
                continue
            text = read(path).decode("utf-8", "surrogateescape")
            # A module file at the crate root sits beside other modules' files.
            if reads_outside(text) or (os.sep not in os.path.relpath(path, types_dir) and INCLUDE.search(text)):
                return None
            refs = references(text, module_path(types_dir, path))
            if refs is None:
                return None
            todo.extend(refs)
    return reached, files


def declared_modules(mod_rs):
    if not os.path.isfile(mod_rs):
        return set()
    return set(MOD_DECL.findall(read(mod_rs).decode("utf-8", "surrogateescape")))


def add_filtered_mod(hasher, path, keep):
    kept = [line for line in read(path).decode("utf-8", "surrogateescape").splitlines()
            if not (m := MOD_DECL.match(line)) or m.group(1) in keep]
    hasher.add("module file " + os.path.relpath(path, hasher.workspace) + ", reached modules",
               "\n".join(kept).encode("utf-8", "surrogateescape"))


def add_rest(hasher, directory, keep, mod_rs):
    """A directory's files less its declared modules not in keep, its mod.rs
    with only their declarations."""
    declared = declared_modules(mod_rs)
    for name in sorted(os.listdir(directory)):
        path = os.path.join(directory, name)
        stem = name[:-3] if name.endswith(".rs") else name
        if name == "target" or stem in keep:
            continue
        if path == mod_rs:
            add_filtered_mod(hasher, path, keep)
        elif stem in declared and (os.path.isdir(path) or name.endswith(".rs")):
            continue
        elif os.path.isdir(path):
            for sub in files_under(path):
                hasher.add_file(sub)
        else:
            hasher.add_file(path)


def add_types(hasher, types_dir, ident, other_files):
    closure = types_closure(types_dir, ident, other_files)
    if closure is None:
        hasher.add("types", b"whole crate")
        for path in files_under(types_dir):
            hasher.add_file(path)
        return
    reached, files = closure
    hasher.add("types units", " ".join(sorted("::".join(u) for u in reached)).encode())
    for path in sorted(set(files)):
        hasher.add_file(path)
    # The crate root and each partly reached top module: their own files, less
    # the modules the step does not reach.
    tops = {u[0] for u in reached}
    add_rest(hasher, types_dir, tops, os.path.join(types_dir, "mod.rs"))
    for top in sorted(tops):
        if (top,) in reached:
            continue
        top_dir = os.path.join(types_dir, top)
        children = {u[1] for u in reached if u[0] == top}
        if os.path.isdir(top_dir):
            add_rest(hasher, top_dir, children, os.path.join(top_dir, "mod.rs"))
        if os.path.isfile(top_dir + ".rs"):
            add_filtered_mod(hasher, top_dir + ".rs", children)


def tree_edges(hasher, tree):
    """The tree as its sorted parent -> child edges: cargo orders a package's
    dependencies by name, and the normalized name sorts elsewhere."""
    edges, stack = set(), []
    for line in tree.splitlines():
        m = re.match(r"^(\d+)(.*?)\s*(?:\(\*\))?\s*$", line)
        if not m:
            continue
        depth, node = int(m.group(1)), hasher.normalize(m.group(2).encode()).decode("utf-8", "surrogateescape")
        del stack[depth:]
        edges.add((stack[-1] if stack else "") + " -> " + node)
        stack.append(node)
    return "\n".join(sorted(edges))


def sections(tree):
    """cargo tree of several -p packages: each root's own tree, by name."""
    out, name = {}, None
    for line in tree.splitlines():
        m = re.match(r"^0(\S+)", line)
        if m:
            name = m.group(1)
            out[name] = []
        if name is not None and line.strip():
            out[name].append(line)
    return out


def one_package(cmd, pkg):
    """CMD with its -p packages replaced by pkg alone, where the first was."""
    out, i, placed = [], 0, False
    while i < len(cmd):
        if cmd[i] in ("-p", "--package") and i + 1 < len(cmd):
            i += 2
        elif cmd[i].startswith("--package="):
            i += 1
        else:
            out.append(cmd[i])
            i += 1
            continue
        if not placed:
            out += ["-p", pkg]
            placed = True
    return out


def root_package(workspace):
    m = re.search(r'^\[package\][^\[]*?^name\s*=\s*"([^"]+)"',
                  read(os.path.join(workspace, "Cargo.toml")).decode(), re.M | re.S)
    return m.group(1) if m else None


def crate_hash(workspace, types_dir, types_names, toolchain, pkg, calls):
    """calls: (command, this package's tree section) for each CMD selecting it."""
    hasher = Hasher(workspace, types_names)
    for label, data in toolchain:
        hasher.add(label, data)
    local = []
    for cmd, section in calls:
        words = []
        for word in one_package(cmd, pkg):
            value = word.split("=", 1)[1] if "=" in word else None
            if value and os.path.isfile(value):
                words.append(word.split("=", 1)[0] + "=<file>")
                hasher.add("env file " + words[-1], read(value))
            else:
                words.append(word)
        hasher.add("command", "\0".join(words).encode())
        hasher.add("tree", tree_edges(hasher, "\n".join(section)).encode())
        for line in section:
            m = TREE_PATH.search(line)
            if m and os.path.realpath(m.group(1)) not in local:
                local.append(os.path.realpath(m.group(1)))

    others = []
    for pkg_dir in local:
        if pkg_dir == types_dir:
            continue
        if not (pkg_dir == workspace or pkg_dir.startswith(workspace + os.sep)):
            sys.exit("rust_selftest_hash.py: local package outside the workspace: " + pkg_dir)
        for path in files_under(pkg_dir):
            hasher.add_file(path)
            others.append(path)
    # A package file that reads another path the tree does not name: the
    # whole workspace.
    if any(path.endswith(".rs") and reads_outside(read(path).decode("utf-8", "surrogateescape"))
           for path in others):
        hasher.add("workspace", b"whole")
        for path in files_under(workspace):
            hasher.add_file(path)
    if types_names and types_dir in local:
        add_types(hasher, types_dir, types_names[1], others)
    return hasher.h.hexdigest()


def main(argv):
    if len(argv) < 3:
        sys.exit("usage: rust_selftest_hash.py WORKSPACE CMD... [::: CMD...]")
    workspace = os.path.realpath(argv[1])
    cmds = [[]]
    for word in argv[2:]:
        if word == ":::":
            cmds.append([])
        else:
            cmds[-1].append(word)
    cmds = [cmd for cmd in cmds if cmd]

    types_dir = os.path.join(workspace, "types")
    types_names = []
    if os.path.isfile(os.path.join(types_dir, "Cargo.toml")):
        name = re.search(r'^name\s*=\s*"([^"]+)"', read(os.path.join(types_dir, "Cargo.toml")).decode(), re.M).group(1)
        if not TYPES_NAME.fullmatch(name):
            sys.exit("rust_selftest_hash.py: unexpected model types crate name: " + name)
        types_names = [name, name.replace("-", "_")]
    toolchain = [("rustc", run(["rustc", "-vV"]).encode()), ("cargo", run(["cargo", "-V"]).encode())]
    root = root_package(workspace)

    calls, order = {}, []
    for cmd in cmds:
        pkgs = packages(cmd)
        if not pkgs:
            sys.exit("rust_selftest_hash.py: the command names no -p package: " + " ".join(cmd))
        tree_cmd = ["cargo", "tree", "-e", "normal,build,dev", "-f", "{p} {f}", "--prefix", "depth"]
        for p in pkgs:
            tree_cmd += ["-p", p]
        trees = sections(run(tree_cmd, cwd=workspace))
        for p in pkgs:
            if p == root:
                continue
            if p not in trees:
                sys.exit("rust_selftest_hash.py: no tree for package " + p)
            if p not in calls:
                calls[p] = []
                order.append(p)
            calls[p].append((cmd, trees[p]))
    for p in order:
        print(p, crate_hash(workspace, types_dir, types_names, toolchain, p, calls[p]))

if __name__ == "__main__":
    main(sys.argv)
