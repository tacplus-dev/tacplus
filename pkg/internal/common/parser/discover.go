package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// protoDirHasProtos reports whether dir exists and contains at least one .proto
// file. Discovery treats an absent/empty proto tree as "no minter declared"
// (the historical text-scan behavior) rather than a hard error; the
// zero-authoritative-when-S2S contract is enforced separately by
// ValidateTokenMinters, which the generators call with knowledge of the S2S
// surface.
func protoDirHasProtos(dir string) bool {
	if dir == "" {
		return false
	}
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && filepath.Ext(path) == ".proto" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// TokenExchangerInfo holds the location of the token mint service, declared
// EXPLICITLY via the (spec.token_minter) service annotation.
type TokenExchangerInfo struct {
	Package                string // proto package name
	Dir                    string // relative dir, e.g. "services/mint"
	ServiceName            string // proto service name, e.g. "Mint"
	TokenMintMethod        string // RPC accepting TokenMintRequest and returning TokenMintResponse
	MintServiceTokenMethod string // RPC accepting MintServiceTokenRequest and returning TokenMintResponse
	// RPC accepting MintExternalServiceTokenRequest and returning
	// TokenMintResponse. Optional: empty when the minter declares none.
	MintExternalServiceTokenMethod string
	X509MintMethod                 string // RPC accepting X509MintRequest and returning X509CertChain
	IsProxy                        bool   // true for the client-reachable C2S front (token_minter {proxy: true})
	// HostLabel is the physical host of a minter declared as a hosted contract
	// ((spec.service_s2s).label, e.g. "kernel" for the sys minter.Minter).
	// Empty when the minter is a service's own, in which case its package is
	// the host.
	HostLabel string
}

// Audience is the token audience for calls to this minter: its host when it is
// a hosted contract, else its own package.
func (t *TokenExchangerInfo) Audience() string {
	if t.HostLabel != "" {
		return t.HostLabel
	}
	return t.Package
}

// SysMinterService is the fully-qualified name of the minting contract tacplus
// declares in sys/minter.proto. It is the authoritative (spec.token_minter) in
// every run; see that file.
const SysMinterService = "minter.Minter"

// scanTokenMinters parses protoDir via the descriptor model and returns every
// service carrying the (spec.token_minter) annotation. The minter is declared
// EXPLICITLY — we do NOT infer it from RPC names (TokenMint/MintServiceToken);
// a service opts in via the annotation, and we resolve its fully-qualified
// package.Service from the parsed descriptor.
//
// Descriptor-based (not a single-line text scan) so a multiline
// `(spec.token_minter) = { proxy: true }` classifies correctly — the text scan
// keyed `proxy` to the same line as the option and silently promoted a proxy
// to the AUTHORITATIVE S2S minter (OF-17a). protoDir must therefore be a
// protoc-resolvable proto tree with spec.* annotations intact (the generators'
// AnnotatedProtoDir), NOT the raw input tree.
//
// As a hard invariant, more than one AUTHORITATIVE (non-proxy) minter is a
// generation error: a build cannot have two services that each claim to be the
// one S2S identity issuer.
func scanTokenMinters(protoDir string) ([]TokenExchangerInfo, error) {
	if !protoDirHasProtos(protoDir) {
		return nil, nil
	}
	dp, err := NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("token-minter discovery: parse %q: %w", protoDir, err)
	}
	minters := dp.TokenMinters()

	authoritative := 0
	for _, m := range minters {
		if !m.IsProxy {
			authoritative++
		}
	}
	if authoritative > 1 {
		var names []string
		for _, m := range minters {
			if !m.IsProxy {
				names = append(names, m.Package+"."+m.ServiceName)
			}
		}
		for _, n := range names {
			if n == SysMinterService {
				return nil, fmt.Errorf("multiple authoritative (spec.token_minter) services declared (%v): the minting contract is declared by tacplus itself (sys/minter.proto, %s); drop (spec.token_minter) and the minting RPCs from the other service and have the token authority host the sys Minter instead", names, SysMinterService)
			}
		}
		return nil, fmt.Errorf("multiple authoritative (spec.token_minter) services declared (%v); exactly one S2S minter is allowed", names)
	}
	return minters, nil
}

// DiscoverTokenExchangerPackage returns the authoritative (S2S) token minter —
// the service annotated (spec.token_minter) = {} (proxy = false). Used by
// service_im: services have an S2S identity and call the minter directly.
// protoDir must be a protoc-resolvable, annotated proto tree.
func DiscoverTokenExchangerPackage(protoDir string) (*TokenExchangerInfo, error) {
	minters, err := scanTokenMinters(protoDir)
	if err != nil {
		return nil, err
	}
	for _, m := range minters {
		if !m.IsProxy {
			mc := m
			return &mc, nil
		}
	}
	return nil, nil
}

// DiscoverClientTokenExchangerPackage returns the client-reachable (C2S) token
// minter — the service annotated (spec.token_minter) = {proxy: true} (e.g. the
// gateway front). Clients (a CLI/daemon, webapp) have no S2S identity and
// cannot call the authoritative minter directly, so they mint through this proxy.
// Falls back to the authoritative minter if no proxy is declared.
func DiscoverClientTokenExchangerPackage(protoDir string) (*TokenExchangerInfo, error) {
	minters, err := scanTokenMinters(protoDir)
	if err != nil {
		return nil, err
	}
	for _, m := range minters {
		if m.IsProxy {
			mc := m
			return &mc, nil
		}
	}
	for _, m := range minters {
		if !m.IsProxy {
			mc := m
			return &mc, nil
		}
	}
	return nil, nil
}

// ValidateTokenMinters enforces the WS-D3/OF-17 generation-time contract: when
// an S2S surface is enabled, the build MUST declare exactly one authoritative
// (non-proxy) (spec.token_minter) service. Zero authoritative minters means the
// generated ServiceManager can never obtain an S2S identity and could never
// start (OF-17b / CONSUMER-6); more than one is ambiguous. Both are hard
// generation errors here rather than a runtime surprise.
//
// protoDir must be a protoc-resolvable, annotated proto tree. s2sEnabled is the
// caller's determination that the build emits any service-to-service surface.
func ValidateTokenMinters(protoDir string, s2sEnabled bool) error {
	minters, err := scanTokenMinters(protoDir)
	if err != nil {
		return err
	}
	if !s2sEnabled {
		return nil
	}
	authoritative := 0
	for _, m := range minters {
		if !m.IsProxy {
			authoritative++
		}
	}
	if authoritative == 0 {
		return fmt.Errorf("an S2S surface is enabled but no authoritative (spec.token_minter) service is declared; the generated ServiceManager cannot obtain an S2S identity")
	}
	return nil
}

// ApprovalBrokerInfo locates the RPC the generated (spec.rpc).requires_device
// check calls: BrokerPendingApproval on the authoritative (spec.token_minter)
// service. The minter is where it lives because every service with an S2S
// identity already holds a generated client to it, so a requires_device RPC
// needs no new route and no new deployment wiring.
type ApprovalBrokerInfo struct {
	Package     string // proto package of the minter, e.g. "kernel"
	Dir         string // relative dir, e.g. "services/kernel"
	ServiceName string // proto service name, e.g. "Mint"
	MethodName  string // the broker RPC, e.g. "BrokerPendingApproval"
}

// DiscoverApprovalBroker returns the approval-broker RPC on the authoritative
// token minter, or nil when the proto set declares no such RPC. The method is
// matched on its SIGNATURE (BrokerPendingApprovalRequest →
// BrokerPendingApprovalResponse), the same way the minter's own token/x509 RPCs
// are found, so nothing here depends on a hardcoded service or method name.
func DiscoverApprovalBroker(protoDir string) (*ApprovalBrokerInfo, error) {
	minter, err := DiscoverTokenExchangerPackage(protoDir)
	if err != nil || minter == nil {
		return nil, err
	}
	dp, err := NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("approval-broker discovery: parse %q: %w", protoDir, err)
	}
	for path, fd := range dp.GetAllFiles() {
		if fd == nil || fd.Package != minter.Package {
			continue
		}
		for _, svc := range fd.Services {
			if svc.Name != minter.ServiceName {
				continue
			}
			for _, m := range svc.Methods {
				if methodSignatureMatches(m, "BrokerPendingApprovalRequest", "BrokerPendingApprovalResponse") {
					return &ApprovalBrokerInfo{
						Package:     fd.Package,
						Dir:         strings.TrimSuffix(path, ".proto"),
						ServiceName: svc.Name,
						MethodName:  m.Name,
					}, nil
				}
			}
		}
	}
	return nil, nil
}
