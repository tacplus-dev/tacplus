// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
)

// The chassis has exactly one JWT verification path that a caller may treat as
// "the" verifier: the Authorizer's verify_bearer, which checks the signature
// AND rejects the token classes that must never reach an API (refresh,
// bootstrap, locality, external) AND requires the audience to be one this
// service accepts.
//
// Every servicemanager auth module used to ship a SECOND, exported verifier
// next to it — go ServiceManager.VerifyToken, java Auth.verifyToken, python
// verify_token, typescript verifyToken — that checked the signature and
// nothing else. No generator-emitted code called any of them; they existed
// only as public SDK surface. A hand-written consumer reaching for the
// obvious-looking "verifyToken" therefore got a check that accepts a refresh
// token, a bootstrap token, or a token minted for a different audience
// entirely, with no hint that it was the weaker of two functions with the
// same name.
//
// (Go additionally exported AuthUnaryInterceptor/AuthStreamInterceptor on top
// of it — a whole weak auth path a consumer could install on its own server.
// They were never installed by generated code either; see
// servicewrapper_caller_from_auth_regression_test.go.)
//
// The invariant: the servicemanager auth surface offers no JWT verifier at
// all. Bearer extraction is fine — it parses a header, it does not decide
// whether a token is trustworthy.
func TestServicemanagerExposesNoSecondJWTVerifier(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		file    string
		banned  []string
		require []string
	}{
		{
			name:    "go",
			dir:     "go/types",
			file:    "servicemanager_lib.go.tmpl",
			banned:  []string{"VerifyToken", "AuthUnaryInterceptor", "AuthStreamInterceptor"},
			require: []string{"func RequestIDFromContext(ctx context.Context) string {"},
		},
		{
			name:    "java",
			dir:     "java/chassis/servicemanager",
			file:    "Auth.java.tmpl",
			banned:  []string{"verifyToken"},
			require: []string{"public static String extractBearerToken(Metadata metadata)"},
		},
		{
			name:    "python",
			dir:     "python/chassis/servicemanager",
			file:    "auth.py.tmpl",
			banned:  []string{"verify_token"},
			require: []string{"def extract_bearer_token("},
		},
		{
			name:    "typescript",
			dir:     "typescript/chassis/servicemanager",
			file:    "Auth.ts.tmpl",
			banned:  []string{"verifyToken"},
			require: []string{"export function extractBearerToken("},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, err := tmplutil.LoadTemplate(tc.dir, tc.file)
			if err != nil {
				t.Fatalf("load %s/%s: %v", tc.dir, tc.file, err)
			}
			for _, b := range tc.banned {
				if strings.Contains(src, b) {
					t.Errorf("%s/%s must not expose %q: a second JWT verifier next to the "+
						"Authorizer's verify_bearer skips the token-class and accepted-audience "+
						"checks, and nothing generated calls it — it is public surface whose only "+
						"effect is to hand a hand-written consumer weaker validation than the name "+
						"promises", tc.dir, tc.file, b)
				}
			}
			for _, r := range tc.require {
				if !strings.Contains(src, r) {
					t.Errorf("%s/%s lost %q — the removal of the weak verifier must not take the "+
						"rest of the auth surface with it", tc.dir, tc.file, r)
				}
			}
		})
	}

	// The re-export barrels must not advertise a verifier either.
	for _, tc := range []struct {
		dir, file, banned string
	}{
		{"python/chassis/servicemanager", "__init__.py.tmpl", "verify_token"},
		{"typescript/chassis/servicemanager", "index.ts.tmpl", "verifyToken"},
	} {
		src, err := tmplutil.LoadTemplate(tc.dir, tc.file)
		if err != nil {
			t.Fatalf("load %s/%s: %v", tc.dir, tc.file, err)
		}
		if strings.Contains(src, tc.banned) {
			t.Errorf("%s/%s still re-exports %q", tc.dir, tc.file, tc.banned)
		}
	}

	// The real verifier is still the Authorizer's, and it still performs both
	// checks the removed one skipped. Without this half the test would pass on
	// a tree where auth had simply been deleted everywhere.
	for _, tc := range []struct {
		name, dir, file string
		want            []string
	}{
		{"go", "go/chassis/grpcserver", "authorizer.go.tmpl",
			[]string{"rejectAPITokenClass(tw)", "acceptedAudiences[tw.GetAudience()]"}},
		{"java", "java/chassis/grpcserver", "Authorizer.java.tmpl",
			[]string{"rejectApiTokenClass(tw)", "ACCEPTED_AUDIENCES"}},
		{"python", "python/chassis/grpcserver", "authorizer.py.tmpl",
			[]string{`"bootstrap", "locality", "external"`, "_ACCEPTED_AUDIENCES"}},
		{"typescript", "typescript/chassis/grpcserver", "Authorizer.ts.tmpl",
			[]string{"tokenUse === 'bootstrap'", "ACCEPTED_AUDIENCES"}},
		{"rust", "rust/chassis/grpcserver/src", "authorizer.rs.tmpl",
			[]string{`token_use == "bootstrap"`, "ACCEPTED_AUDIENCES"}},
	} {
		src, err := tmplutil.LoadTemplate(tc.dir, tc.file)
		if err != nil {
			t.Fatalf("load %s/%s: %v", tc.dir, tc.file, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(src, w) {
				t.Errorf("%s authorizer lost %q — the one real verifier must keep the "+
					"token-class and accepted-audience checks", tc.name, w)
			}
		}
	}
}
