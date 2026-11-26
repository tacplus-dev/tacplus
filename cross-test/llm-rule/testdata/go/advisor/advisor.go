// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package advisor is a green advisor (#909): it reaches its model only through
// the generated model port, the one package of the service it imports.
package advisor

import (
	"context"

	port "gitlab.com/example/dummy/advised/model/advisor"
)

// Ask asks the model through the port, on the trace of the request it answers.
func Ask(ctx context.Context, model port.ModelPort, traceID, prompt string) (string, error) {
	resp, err := model.Generate(ctx, traceID, &port.CompleteRequest{Prompt: prompt})
	if err != nil {
		return "", err
	}
	return resp.GetText(), nil
}
