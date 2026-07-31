// Copyright (c) 2026 Second Star Cavalier LLC

package types

// ErrorsTs returns the content of the errors.ts template loaded from the embedded filesystem.
func ErrorsTs() (string, error) {
	return loadTemplate("errors.ts.tmpl")
}
