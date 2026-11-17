#!/usr/bin/env bash
# Shipped in the base and build images as /usr/local/bin/tacplus-packages-check
# (#554). An image carries no Debian package beyond its declared lists
# (images/base.packages, images/build.packages), Debian's base system
# (Essential, Priority required or important) and what those depend on
# (Depends, Pre-Depends; any installed alternative, a virtual name through what
# provides it). Each Dockerfile runs it last, so a package that creeps in, as
# OpenEXR did through buildpack-deps' ImageMagick (CVE-2026-42216/-42217),
# fails the image's build. A declared package that is not installed fails it
# too: the list says what the image carries.
#
# Usage:  tacplus-packages-check DECLARED...
# TACPLUS_DPKG_STATUS: the dpkg status file (default /var/lib/dpkg/status).
set -euo pipefail
(( $# > 0 )) || { echo "usage: tacplus-packages-check DECLARED..." >&2; exit 2; }
status=${TACPLUS_DPKG_STATUS:-/var/lib/dpkg/status}
[[ -r $status ]] || { echo "tacplus-packages-check: cannot read $status" >&2; exit 2; }
found=$(awk -v status="$status" '
  # The declared lists: one package per line, # starts a comment.
  FILENAME != status { sub(/#.*/, ""); for (i = 1; i <= NF; i++) declared[$i] = 1; next }
  /^$/ { stanza(); next }
  /^[A-Za-z-]+:/ { name = $0; sub(/:.*/, "", name); value = $0; sub(/^[^:]*:[ \t]*/, "", value); f[name] = value }
  END {
    stanza()
    for (d in declared) {
      if (!(d in provider)) { print "declared but not installed: " d; continue }
      reach(d)
    }
    for (p in installed) if (root[p]) reach(p)
    while (n > 0) {
      p = queue[n--]
      groups = split(deps[p], group, ",")
      for (g = 1; g <= groups; g++) {
        alts = split(group[g], alt, "|")
        for (a = 1; a <= alts; a++) {
          d = alt[a]; sub(/\(.*/, "", d); gsub(/[ \t]/, "", d); sub(/:.*/, "", d)
          if (d != "") reach(d)
        }
      }
    }
    for (p in installed) if (!(p in seen)) print "undeclared: " p
  }
  function stanza(   s, k, i, v) {
    if (f["Package"] != "" && f["Status"] ~ / installed$/) {
      s = f["Package"]
      installed[s] = 1
      provider[s] = provider[s] " " s
      deps[s] = f["Pre-Depends"] ", " f["Depends"]
      root[s] = f["Essential"] == "yes" || f["Priority"] == "required" || f["Priority"] == "important"
      k = split(f["Provides"], prov, ",")
      for (i = 1; i <= k; i++) { v = prov[i]; sub(/\(.*/, "", v); gsub(/[ \t]/, "", v); if (v != "") provider[v] = provider[v] " " s }
    }
    delete f
  }
  # Queue every installed package that provides name, once.
  function reach(name,   k, i, ps) {
    k = split(provider[name], ps, " ")
    for (i = 1; i <= k; i++) if (!(ps[i] in seen)) { seen[ps[i]] = 1; queue[++n] = ps[i] }
  }
' "$@" "$status" | sort)
if [[ -n $found ]]; then
  echo "$found" >&2
  echo "tacplus-packages-check: the image's Debian packages differ from its declared lists ($*); add a package the image needs to its list (images/*.packages), or remove what pulls it in" >&2
  exit 1
fi
echo "tacplus-packages-check: OK ($(grep -c '^Status: .* installed$' "$status") packages)"
