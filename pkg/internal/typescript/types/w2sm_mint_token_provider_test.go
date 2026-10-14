// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// The only generated mint chain (chassis/client_im ClientIdentityManager)
// imports @grpc/grpc-js and cannot run in a browser, so web apps hand-rolled
// refresh -> access -> service minting over the W2S MintClient. w2sm generates
// MintTokenProvider over that client when the client-reachable token minter is
// one of the package's W2S client targets.
func TestTypeScriptW2SMintTokenMethod(t *testing.T) {
	minter := &parser.TokenExchangerInfo{Package: "gateway", ServiceName: "Mint", TokenMintMethod: "MintToken", IsProxy: true}
	gatewayMint := service.ServiceInfo{PackageName: "gateway", ServiceName: "Mint"}
	other := service.ServiceInfo{PackageName: "scottypersonal", ServiceName: "ScottyPersonal"}

	cases := []struct {
		name   string
		w2s    []service.ServiceInfo
		minter *parser.TokenExchangerInfo
		want   string
	}{
		{"minter is a w2s target", []service.ServiceInfo{other, gatewayMint}, minter, "mintToken"},
		{"minter not a w2s target", []service.ServiceInfo{other}, minter, ""},
		{"no minter declared", []service.ServiceInfo{gatewayMint}, nil, ""},
		{"same service name, other package", []service.ServiceInfo{{PackageName: "kernel", ServiceName: "Mint"}}, minter, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := w2sMintTokenMethod(tc.w2s, tc.minter); got != tc.want {
				t.Fatalf("w2sMintTokenMethod = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTypeScriptW2SMintTokenProviderRender(t *testing.T) {
	render := func(t *testing.T, method string) string {
		t.Helper()
		outDir := t.TempDir()
		if err := generateW2SServiceManager(&tactype.Config{OutputDir: outDir}, method); err != nil {
			t.Fatalf("generateW2SServiceManager: %v", err)
		}
		b, err := os.ReadFile(filepath.Join(outDir, "w2sm", "index.ts"))
		if err != nil {
			t.Fatalf("read w2sm/index.ts: %v", err)
		}
		return string(b)
	}

	t.Run("with minter", func(t *testing.T) {
		out := render(t, "mintToken")
		for _, want := range []string{
			"import { create } from '@bufbuild/protobuf';",
			"} from '../types/sys/tokenrequest/index.js';",
			"import { TokenPurpose } from '../types/sys/token/index.js';",
			"export const W2S_REFRESH_TOKEN_KEY = 'w2sm.refresh_token';",
			"export interface TokenStorage {",
			"  mintToken(request: TokenMintRequest): Promise<TokenMintResponse>;",
			"export class MintTokenProvider {",
			"  constructor(mint: TokenMinter, storage: TokenStorage) {",
			"  readonly provider: TokenProvider = (service?: string) => this.getTokenFor(service ?? '');",
			// refresh -> access (empty aud) -> service-specific, batched like ClientIdentityManager.
			"resp = await this.mint.mintToken(create(TokenMintRequestSchema, {\n        inputToken,\n        tokenPurpose: TokenPurpose.ACCESS,\n        serviceAuds,\n      }));",
			"const { access } = await this.mintCall(refresh, []);",
			"const { access, services } = await this.mintCall(input, [audience]);",
			// A refused refresh ends the session.
			"if (e instanceof W2SError && e.code === W2S_UNAUTHENTICATED) {",
			"this.storage.removeItem(W2S_REFRESH_TOKEN_KEY);",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("w2sm/index.ts missing %q", want)
			}
		}
		if strings.Contains(out, "@grpc/grpc-js") {
			t.Error("w2sm must stay browser-safe: no @grpc/grpc-js import")
		}
	})

	t.Run("without minter", func(t *testing.T) {
		out := render(t, "")
		for _, banned := range []string{"MintTokenProvider", "sys/tokenrequest", "@bufbuild/protobuf"} {
			if strings.Contains(out, banned) {
				t.Errorf("w2sm/index.ts without a W2S minter must not carry %q", banned)
			}
		}
		if !strings.Contains(out, "export class ServiceManager {") {
			t.Error("w2sm/index.ts lost ServiceManager")
		}
	})
}
