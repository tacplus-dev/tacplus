// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package publicsuffix is the one source of (spec.assert_registrable_domain)
// (tacplus#224): the Public Suffix List snapshot tacplus embeds, its rules in
// the ASCII form every generated validator matches, and the reference check
// the Go, Java, Rust, Python and TypeScript emitters reproduce.
//
// public_suffix_list.dat is https://publicsuffix.org/list/public_suffix_list.dat
// unmodified (MPL-2.0, its header says so); its VERSION and COMMIT lines say
// which snapshot it is. Moving it is a deliberate commit: download the list
// over it, and the tests here and the generated validators follow.
package publicsuffix

import (
	_ "embed"
	"errors"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

//go:embed public_suffix_list.dat
var listData string

var (
	rulesOnce sync.Once
	rules     []string
	ruleSet   map[string]bool
)

// Rules returns the list's rules, ICANN and private sections together, in
// ASCII (each Unicode label in its xn-- form), lowercase, sorted and without
// duplicates. A wildcard keeps its "*." prefix and an exception its "!".
// Every generated validator embeds exactly this table.
func Rules() []string {
	load()
	return append([]string(nil), rules...)
}

func load() {
	rulesOnce.Do(func() {
		seen := map[string]bool{}
		for _, line := range strings.Split(listData, "\n") {
			// A rule is the line up to its first whitespace; comments and
			// blank lines carry none.
			fields := strings.Fields(line)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "//") {
				continue
			}
			rule, err := toASCII(strings.ToLower(fields[0]))
			if err != nil {
				panic("publicsuffix: embedded rule " + fields[0] + ": " + err.Error())
			}
			seen[rule] = true
		}
		for r := range seen {
			rules = append(rules, r)
		}
		sort.Strings(rules)
		ruleSet = seen
	})
}

// Version is the snapshot's VERSION line, e.g. "2026-11-06_09-01-47_UTC".
func Version() string {
	for _, line := range strings.Split(listData, "\n") {
		if v, ok := strings.CutPrefix(line, "// VERSION: "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// IsPublicSuffix reports whether name, lowercase ASCII without a trailing
// dot, is a public suffix: a single label (the list's implicit "*" rule), a
// rule, or under a wildcard rule without an exception.
func IsPublicSuffix(name string) bool {
	load()
	if !strings.Contains(name, ".") {
		return true
	}
	if ruleSet["!"+name] {
		return false
	}
	if ruleSet[name] {
		return true
	}
	_, parent, _ := strings.Cut(name, ".")
	return ruleSet["*."+parent]
}

// Errors of CheckRegistrable, the reference for the generated validators.
var (
	ErrNotDomain    = errors.New("not a domain name")
	ErrPublicSuffix = errors.New("a public suffix")
)

// CheckRegistrable is the (spec.assert_registrable_domain) check: nil when
// value is a domain name someone can register, ErrNotDomain when it is not a
// domain name in the accepted form, ErrPublicSuffix when it is a public
// suffix. Generated validators skip an empty value before calling it.
func CheckRegistrable(value string) error {
	if len(value) == 0 || len(value) > 253 {
		return ErrNotDomain
	}
	labels := strings.Split(value, ".")
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return ErrNotDomain
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return ErrNotDomain
			}
		}
	}
	if IsPublicSuffix(strings.ToLower(value)) {
		return ErrPublicSuffix
	}
	return nil
}

// toASCII converts each non-ASCII label of a rule to its xn-- form
// (RFC 3492 Punycode); the list's Unicode rules are already in the
// normalized, lowercase form IDNA maps to.
func toASCII(rule string) (string, error) {
	labels := strings.Split(rule, ".")
	for i, l := range labels {
		ascii := true
		for j := 0; j < len(l); j++ {
			if l[j] >= utf8.RuneSelf {
				ascii = false
				break
			}
		}
		if ascii {
			continue
		}
		enc, err := punycode(l)
		if err != nil {
			return "", err
		}
		labels[i] = "xn--" + enc
	}
	return strings.Join(labels, "."), nil
}

// punycode is RFC 3492's encoder (section 6.3) for one label.
func punycode(label string) (string, error) {
	const (
		base        = 36
		tmin        = 1
		tmax        = 26
		skew        = 38
		damp        = 700
		initialBias = 72
		initialN    = 128
	)
	if !utf8.ValidString(label) {
		return "", errors.New("invalid UTF-8")
	}
	input := []rune(label)
	var out strings.Builder
	for _, r := range input {
		if r < 0x80 {
			out.WriteRune(r)
		}
	}
	b := out.Len()
	h := b
	if b > 0 {
		out.WriteByte('-')
	}
	digit := func(d int) byte {
		if d < 26 {
			return byte('a' + d)
		}
		return byte('0' + d - 26)
	}
	adapt := func(delta, numPoints int, first bool) int {
		if first {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / numPoints
		k := 0
		for delta > ((base-tmin)*tmax)/2 {
			delta /= base - tmin
			k += base
		}
		return k + (base-tmin+1)*delta/(delta+skew)
	}
	n, delta, bias := initialN, 0, initialBias
	for h < len(input) {
		m := int(^uint(0) >> 1)
		for _, r := range input {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range input {
			if int(r) < n {
				delta++
			}
			if int(r) == n {
				q := delta
				for k := base; ; k += base {
					t := k - bias
					if t < tmin {
						t = tmin
					} else if t > tmax {
						t = tmax
					}
					if q < t {
						break
					}
					out.WriteByte(digit(t + (q-t)%(base-t)))
					q = (q - t) / (base - t)
				}
				out.WriteByte(digit(q))
				bias = adapt(delta, h+1, h == b)
				delta = 0
				h++
			}
		}
		delta++
		n++
	}
	return out.String(), nil
}
