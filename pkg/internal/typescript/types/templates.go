// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

// ErrorsTs returns the content of the errors.ts template loaded from the embedded filesystem.
func ErrorsTs() (string, error) {
	return loadTemplate("errors.ts.tmpl")
}
