// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import "fmt"

// A project says what it is (Jody, 2026-10-23: "the tacplus.yaml might should
// say if something is a service, client, or library"):
//
//	kind: service   # hosts a surface and has a registry identity; a fleet runs it
//	kind: client    # calls services and hosts nothing (a CLI, a daemon, an app)
//	kind: library   # contracts only: protos other projects build against
//
// Today the generator infers all of this from annotations. The declaration is
// the consumer's statement of intent, so a mismatch (a "client" that declares
// a hosted service, a "library" with an implementation) can be refused rather
// than guessed at, and so the fleet build knows what an entry is without
// reading its protos.
const (
	KindService = "service"
	KindClient  = "client"
	KindLibrary = "library"
)

// CheckKind rejects an unknown kind. Empty is allowed: not stated.
func CheckKind(kind string) error {
	switch kind {
	case "", KindService, KindClient, KindLibrary:
		return nil
	}
	return fmt.Errorf("%s: kind %q must be %q, %q or %q", Filename, kind, KindService, KindClient, KindLibrary)
}
