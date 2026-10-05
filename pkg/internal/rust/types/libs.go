// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/keepaliveconst"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	commonspiffe "gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/spiffe"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// serviceRPCInfo holds per-service RPC enum data for the authorizer template.
type serviceRPCInfo struct {
	FieldName     string // Rust struct field name on EntitlementProfile, e.g. "sentinel"
	ProstEnumType string // prost-generated enum type, e.g. "SentinelRpCs"
}

// toProstRPCEnumName converts a service enum base name like "Sentinel" to the
// prost-mangled PascalCase form of the RPCs enum, e.g. "SentinelRpCs".
// The proto enum is {EnumName}RPCs; prost converts each run of uppercase
// letters: "RPCs" → "Rp" + "Cs".
func toProstRPCEnumName(enumName string) string {
	protoName := enumName + "RPCs"
	return strutil.ToTonicCamelCase(protoName)
}

// writeLibTemplates processes the embedded Rust library templates and writes them
// to the output directory at {langDir}/chassis/
func writeLibTemplates(config *tactype.Config, svcRPCs []proto.ServiceRPCEntry, acceptedAudiences []string) error {
	langDir := config.LangDir()
	sysBaseDir := filepath.Join(langDir, "chassis")

	zap.L().Info("Writing Rust library templates", zap.String("dir", sysBaseDir))

	// Clean old sys directory to remove stale crates (e.g., tokencache, protoserver)
	if err := os.RemoveAll(sysBaseDir); err != nil {
		return fmt.Errorf("failed to clean sys directory: %w", err)
	}

	// Two distinct token-mint exchangers, because client_im and service_im have
	// different reachability constraints:
	//
	//   * service_im (services) calls MintServiceToken on the authoritative S2S
	//     mint — services have an S2S identity and call it directly.
	//   * client_im (clients: a CLI/daemon, etc.) have no S2S identity and
	//     cannot call the authoritative S2S mint directly. They mint via the
	//     client-reachable C2S endpoint, which can proxy with its own S2S
	//     identity to reach the authority — the same path the webapp uses.
	//
	// Using one shared exchanger for both breaks one of them when the
	// authoritative mint has no C2S route for clients and the client-facing
	// mint has no MintServiceToken for services. So discover them separately.
	texPkg := ""
	mintServiceName := ""
	hasTokenExchanger := false
	info, err := parser.DiscoverTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return err
	}
	if info != nil {
		texPkg = info.Package
		mintServiceName = info.ServiceName
		hasTokenExchanger = true
	}
	defaultAudience := config.ServiceLabel()
	if hasTokenExchanger {
		defaultAudience = texPkg
	}
	// Derive Rust client module/type names from proto service name.
	// e.g. "Mint" → module "mint_client", type "MintClient"
	mintClientModule := ""
	// Use ToTonicCamelCase for the tonic-generated client struct name.
	mintClientType := ""
	if hasTokenExchanger {
		mintClientModule = tonicServiceModuleName(mintServiceName) + "_client"
		mintClientType = tonicServiceTypeName(mintServiceName) + "Client"
	}
	mintServiceTokenMethod := ""
	if hasTokenExchanger {
		if info.MintServiceTokenMethod == "" {
			return fmt.Errorf("token minter %s.%s must define an RPC accepting MintServiceTokenRequest and returning TokenMintResponse", info.Package, info.ServiceName)
		}
		mintServiceTokenMethod = strutil.ToSnakeCase(info.MintServiceTokenMethod)
	}
	// Optional: empty when the minter declares no external-audience mint, and
	// the generated token_for_external then reports that instead of minting.
	mintExternalServiceTokenMethod := ""
	if hasTokenExchanger && info.MintExternalServiceTokenMethod != "" {
		mintExternalServiceTokenMethod = strutil.ToSnakeCase(info.MintExternalServiceTokenMethod)
	}

	// client_im exchanger — prefers the gateway C2S proxy. Defaults to the
	// authoritative values if no client-reachable mint is found.
	clientTexPkg := ""
	clientMintServiceName := ""
	hasClientTokenExchanger := false
	clientInfo, err := parser.DiscoverClientTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return err
	}
	if clientInfo != nil {
		clientTexPkg = clientInfo.Package
		clientMintServiceName = clientInfo.ServiceName
		hasClientTokenExchanger = true
	}
	clientMintClientModule := ""
	clientMintClientType := ""
	if hasClientTokenExchanger {
		clientMintClientModule = tonicServiceModuleName(clientMintServiceName) + "_client"
		clientMintClientType = tonicServiceTypeName(clientMintServiceName) + "Client"
	}
	clientTokenMintMethod := ""
	if hasClientTokenExchanger {
		if clientInfo.TokenMintMethod == "" {
			return fmt.Errorf("client token minter %s.%s must define an RPC accepting TokenMintRequest and returning TokenMintResponse", clientInfo.Package, clientInfo.ServiceName)
		}
		clientTokenMintMethod = strutil.ToSnakeCase(clientInfo.TokenMintMethod)
	}

	// Does this proto set declare the durable accounting record? Client repos
	// and any build without platform/proto/vservices do not, and the chassis
	// accounting sink is gated on it — EventBusSink cannot name a type that
	// was never generated.
	hasAccountingRecord := false
	if config.AnnotatedProtoDir != "" {
		dp, err := parser.NewDescriptorParser(config.AnnotatedProtoDir)
		if err != nil {
			return fmt.Errorf("create descriptor parser for accounting record detection: %w", err)
		}
		_, hasAccountingRecord, err = boundaries.LookupAccountingRecordTopic(dp)
		if err != nil {
			return fmt.Errorf("resolve accounting record topic: %w", err)
		}
	}

	// Build per-service RPC info for the authorizer template.
	var svcRPCInfos []serviceRPCInfo
	for _, svc := range svcRPCs {
		svcRPCInfos = append(svcRPCInfos, serviceRPCInfo{
			FieldName:     svc.GrpcName,
			ProstEnumType: toProstRPCEnumName(svc.EnumName),
		})
	}

	// Template data
	data := struct {
		CrateBasePath           string
		CrateBasePathUnderscore string
		TokenExchangerPackage   string // authoritative (S2S) mint package — used by service_im
		MintServiceName         string // proto service name, e.g. "Mint"
		MintClientModule        string // Rust module name, e.g. "mint_client"
		MintClientType          string // Rust type name, e.g. "MintClient"
		MintServiceTokenMethod  string // Rust method name for MintServiceTokenRequest exchange
		// Rust method name for MintExternalServiceTokenRequest; empty if undeclared.
		MintExternalServiceTokenMethod string
		HasTokenExchanger              bool
		DefaultAudience                string
		// Client-facing (C2S-reachable) mint — used by client_im. May differ from
		// the authoritative one (e.g. a gateway Mint proxy vs the S2S Mint).
		ClientTokenExchangerPackage string
		ClientMintClientModule      string
		ClientMintClientType        string
		ClientTokenMintMethod       string
		HasClientTokenExchanger     bool
		ServiceRPCs                 []serviceRPCInfo // per-service RPC enum info for authorizer
		HasAccountingRecord         bool             // proto set declares accountingrecord.AccountingRecord
		ServiceLabel                string           // service identity baked into Authorizer
		AcceptedAudiences           []string         // every `aud` the Authorizer accepts: own label + hosted traits
		Spiffe                      commonspiffe.TemplateGrammar
		KeepaliveTimeSecs           int
		KeepaliveTimeoutSecs        int
		KeepalivePermitMinTimeSecs  int
	}{
		CrateBasePath:                  config.CrateBasePath,
		CrateBasePathUnderscore:        strings.ReplaceAll(config.CrateBasePath, "-", "_"),
		TokenExchangerPackage:          texPkg,
		MintServiceName:                mintServiceName,
		MintClientModule:               mintClientModule,
		MintClientType:                 mintClientType,
		MintServiceTokenMethod:         mintServiceTokenMethod,
		MintExternalServiceTokenMethod: mintExternalServiceTokenMethod,
		HasTokenExchanger:              hasTokenExchanger,
		DefaultAudience:                defaultAudience,
		ClientTokenExchangerPackage:    clientTexPkg,
		ClientMintClientModule:         clientMintClientModule,
		ClientMintClientType:           clientMintClientType,
		ClientTokenMintMethod:          clientTokenMintMethod,
		HasClientTokenExchanger:        hasClientTokenExchanger,
		ServiceRPCs:                    svcRPCInfos,
		HasAccountingRecord:            hasAccountingRecord,
		ServiceLabel:                   config.ServiceLabel(),
		AcceptedAudiences:              acceptedAudiences,
		Spiffe:                         commonspiffe.TemplateData(),
		KeepaliveTimeSecs:              keepaliveconst.TimeSecs,
		KeepaliveTimeoutSecs:           keepaliveconst.TimeoutSecs,
		KeepalivePermitMinTimeSecs:     keepaliveconst.PermitMinTimeSecs,
	}

	langFS, err := fs.Sub(templates.FS, "rust/chassis")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	// Walk all embedded template files
	err = fs.WalkDir(langFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".tmpl") {
			return nil
		}

		// Read template content
		content, err := fs.ReadFile(langFS, path)
		if err != nil {
			return fmt.Errorf("failed to read template %s: %w", path, err)
		}

		// Parse and execute template
		tmpl, err := template.New(path).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", path, err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute template %s: %w", path, err)
		}

		// Output path: strip .tmpl suffix
		outputFile := strings.TrimSuffix(path, ".tmpl")
		outputPath := filepath.Join(sysBaseDir, outputFile)

		// Create directory
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", outputPath, err)
		}

		// Write file
		if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputPath, err)
		}

		zap.L().Debug("Wrote Rust library file", zap.String("path", outputFile))
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to write Rust library templates: %w", err)
	}

	zap.L().Info("Rust library templates written successfully")
	return nil
}
