// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package publicsuffix

// Vector is one (spec.assert_registrable_domain) case: Want is nil for a
// registrable name, else the error CheckRegistrable returns.
type Vector struct {
	In   string
	Want error
}

// Vectors are the cases every language's generated validator is held to
// (the emitters' executable tests run them through each toolchain).
var Vectors = []Vector{
	{"example.com", nil},
	{"Example.COM", nil},
	{"sub.example.com", nil},
	{"example.co.uk", nil},
	{"acme.github.io", nil},
	{"www.ck", nil},             // exception rule !www.ck
	{"xn--55qx5d.example", nil}, // no rule for "example": registrable under the implicit "*"
	{"kawasaki.jp", nil},        // *.kawasaki.jp does not cover kawasaki.jp itself
	{"com", ErrPublicSuffix},
	{"co.uk", ErrPublicSuffix},
	{"CO.UK", ErrPublicSuffix},
	{"github.io", ErrPublicSuffix},
	{"foo.ck", ErrPublicSuffix}, // wildcard *.ck
	{"xn--55qx5d.cn", ErrPublicSuffix},
	{"example", ErrPublicSuffix}, // one label
	{"example.com.", ErrNotDomain},
	{".example.com", ErrNotDomain},
	{"exa mple.com", ErrNotDomain},
	{"-a.example.com", ErrNotDomain},
	{"a-.example.com", ErrNotDomain},
	{"user@example.com", ErrNotDomain},
	{"公司.cn", ErrNotDomain},
	{"a..com", ErrNotDomain},
}

// Outcome is the word a generated validator's test prints for a vector:
// "accept", or the refusal's text, "not a domain name" or "a public suffix",
// which every language's validator carries in its error.
func (v Vector) Outcome() string {
	if v.Want == nil {
		return "accept"
	}
	return v.Want.Error()
}

// AddressVectors are the (spec.assert_not_address) cases: true when refused.
var AddressVectors = []struct {
	In      string
	Refused bool
}{
	{"248289761001", false},
	{"00u1abcd2EFGH3ijk4x7", false},
	{"AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", false},
	{"jody@acme.com", true},
	{"user@contoso.onmicrosoft.com", true},
	{"@", true},
}
