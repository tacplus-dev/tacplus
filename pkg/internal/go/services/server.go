// Copyright (c) 2026 Second Star Cavalier LLC

package services

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/packages"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
)

// GoServerGenerator generates an example Go gRPC server that wraps with lib
type GoServerGenerator struct {
	Name                  string
	ProtoInfo             *parser.ProtoInfo
	OutputDir             string
	GeneratedModule       string // The Go module path for the generated proto code
	TypesModule           string // The Go module path for the proto types (optional, auto-derived if empty)
	ServicesModule        string // The Go module path for the gRPC services package (optional, auto-derived if empty)
	GrpcServerModule      string // The Go module path for the gRPC server/servicewrapper package (optional, auto-derived if empty)
	LibGrpcServerModule   string // The Go module path for the grpcserver lib (e.g., "{base}/chassis/grpcserver")
	GrpcServerTypesModule string // The Go module path for grpcserver types (e.g., "{base}/types/chassis/grpcserver/types")
	WrapperFuncName       string // The wrapper constructor name: "NewFrontendWrapper" or "NewStateWrapper"
	InternalImportBase    string // Base path for internal/ imports (optional, derived from GeneratedModule if empty)
	VersionTag            string // Version tag for generated artifacts (e.g., "1.0.1" or "v1.0.1")
	SkipGoMod             bool   // Skip generating go.mod (when part of a parent module)
}

// Generate creates the example Go server files
func (g *GoServerGenerator) Generate() error {
	// Ensure TypesModule is set (for template usage)
	if g.TypesModule == "" {
		g.TypesModule = g.GeneratedModule
	}
	// Ensure ServicesModule is set (for template usage)
	if g.ServicesModule == "" {
		g.ServicesModule = g.GeneratedModule
	}
	// Ensure GrpcServerModule is set (for template usage)
	if g.GrpcServerModule == "" {
		g.GrpcServerModule = g.ServicesModule
	}
	// Ensure LibGrpcServerModule is set (for template usage)
	if g.LibGrpcServerModule == "" {
		// Default: derive from GeneratedModule by finding base and appending /chassis/grpcserver
		g.LibGrpcServerModule = g.GeneratedModule + "/chassis/grpcserver"
	}
	// Ensure GrpcServerTypesModule is set (for template usage)
	if g.GrpcServerTypesModule == "" {
		g.GrpcServerTypesModule = g.GeneratedModule + "/chassis/grpcserver/types"
	}
	// Ensure WrapperFuncName is set
	if g.WrapperFuncName == "" {
		g.WrapperFuncName = "NewFrontendWrapper"
	}

	// Create internal subdirectories
	internalDir := filepath.Join(g.OutputDir, "internal")
	serverDir := filepath.Join(internalDir, "server")
	clientDir := filepath.Join(internalDir, "client")

	if err := os.MkdirAll(serverDir, 0755); err != nil {
		return fmt.Errorf("failed to create internal/server directory: %w", err)
	}
	if err := os.MkdirAll(clientDir, 0755); err != nil {
		return fmt.Errorf("failed to create internal/client directory: %w", err)
	}

	// Create main.go (entry point)
	if err := g.generateMain(); err != nil {
		return err
	}

	// Create internal/server/server.go (server setup)
	if err := g.generateInternalServer(); err != nil {
		return err
	}

	// Create internal/server/service.go (service implementation)
	if err := g.generateInternalService(); err != nil {
		return err
	}

	// Create internal/client/client.go (client implementation)
	if err := g.generateInternalClient(); err != nil {
		return err
	}

	// Create README.md
	if err := g.generateReadme(); err != nil {
		return err
	}

	// Create go.mod (skip when part of a parent module)
	if !g.SkipGoMod {
		if err := g.generateGoMod(); err != nil {
			return err
		}
	}

	// Create config.yaml
	if err := g.generateConfig(); err != nil {
		return err
	}

	return nil
}

func (g *GoServerGenerator) generateMain() error {
	// Determine base module for internal imports
	baseModule := g.InternalImportBase
	if baseModule == "" {
		// Legacy: extract first path component from GeneratedModule
		baseModule = g.GeneratedModule
		if idx := strings.Index(baseModule, "/"); idx != -1 {
			baseModule = baseModule[:idx]
		}
	}

	header := header.GeneratedHeader(consts.LangGo, "")
	tmpl := header + `// Usage:
//   ./app server  - Start the server
//   ./app client  - Run the client

package main

import (
	"fmt"
	"os"

	"{{.BaseModule}}/internal/server"
	"{{.BaseModule}}/internal/client"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: " + os.Args[0] + "<server|client>")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("server - Start the gRPC server")
		fmt.Println("client - Run the gRPC client")
		os.Exit(1)
	}

	command := os.Args[1]

	var err error
	switch command {
	case "server":
		err = server.Run()
	case "client":
		client.Run()
	default:
		fmt.Printf("Unknown command: %s\n", command)
		fmt.Println("Use 'server' or 'client'")
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
`

	t, err := template.New("main").Parse(tmpl)
	if err != nil {
		return err
	}

	// Prepare template data with base module
	data := struct {
		BaseModule      string
		GeneratedModule string
		TypesModule     string
		ServicesModule  string
		ProtoInfo       *parser.ProtoInfo
	}{
		BaseModule:      baseModule,
		GeneratedModule: g.GeneratedModule,
		TypesModule:     g.TypesModule,
		ServicesModule:  g.ServicesModule,
		ProtoInfo:       g.ProtoInfo,
	}

	// Execute template to buffer first
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return err
	}

	// Format the generated code with gofmt
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format generated code: %w", err)
	}

	// Write formatted code to file
	return os.WriteFile(filepath.Join(g.OutputDir, "main.go"), formatted, 0644)
}

func (g *GoServerGenerator) generateInternalServer() error {
	header := header.GeneratedHeader(consts.LangGo, "")
	tmpl := header + `// This file contains the server setup and initialization logic.
// It handles configuration loading, service creation, and server lifecycle management.

package server

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"{{.LibGrpcServerModule}}"
	fsconfig "{{.GrpcServerTypesModule}}"
	fe "{{.GrpcServerModule}}"
)

// Run starts the server and blocks until shutdown
func Run() error {
	// Load configuration
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml"
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Load grpcserver config
	config, err := fsconfig.ConfigFromBytes(configData)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Create our gRPC service implementation
	grpcService := NewServiceImplementation()

	// Create authorizer from config (nil JwkView = no token signature verification)
	auth, err := grpcserver.NewAuthorizer(config, nil)
	if err != nil {
		return fmt.Errorf("failed to create authorizer: %w", err)
	}

	// Create service wrapper that provides REST support
	wrapper := fe.{{.WrapperFuncName}}(grpcService, auth)

	// Create grpcserver instance with the wrapped service
	srv, err := grpcserver.New(config, wrapper)
	if err != nil {
		return fmt.Errorf("failed to create grpcserver: %w", err)
	}

	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT (Ctrl+C) and SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		fmt.Printf("Received signal: %v\n", sig)
		fmt.Println("Shutting down gracefully...")
		cancel()
	}()

	// Start the server (blocks until shutdown)
	if err := srv.Listen(ctx); err != nil {
		// If context was cancelled, this is a graceful shutdown
		if ctx.Err() != nil {
			fmt.Println("Server stopped")
			return nil
		}
		// Otherwise, it's an actual error
		return fmt.Errorf("server error: %w", err)
	}

	fmt.Println("Server stopped")
	return nil
}
`

	t, err := template.New("internal-server").Parse(tmpl)
	if err != nil {
		return err
	}

	// Execute template to buffer first
	var buf bytes.Buffer
	if err := t.Execute(&buf, g); err != nil {
		return err
	}

	// Format the generated code with gofmt
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format generated code: %w", err)
	}

	// Write formatted code to file
	return os.WriteFile(filepath.Join(g.OutputDir, "internal", "server", "server.go"), formatted, 0644)
}

// svcPrefix returns "pb" when types and services are in the same Go package,
// "svc" when they are separate packages.
func (g *GoServerGenerator) svcPrefix() string {
	if g.TypesModule == g.ServicesModule {
		return "pb"
	}
	return "svc"
}

func (g *GoServerGenerator) generateInternalService() error {
	var methods strings.Builder
	var embeds strings.Builder

	prefix := g.svcPrefix()
	for _, service := range g.ProtoInfo.Services {
		fmt.Fprintf(&embeds, "%s.Unimplemented%sServer\n", prefix, service.Name)
		for _, method := range service.Methods {
			methodImpl := g.generateMethodStub(service, method)
			methods.WriteString(methodImpl)
			methods.WriteString("\n")
		}
	}

	var importBlock string
	if g.TypesModule == g.ServicesModule {
		importBlock = fmt.Sprintf("\tpb %q", g.TypesModule)
	} else {
		importBlock = fmt.Sprintf("\tpb %q\n\tsvc %q", g.TypesModule, g.ServicesModule)
	}

	content := fmt.Sprintf(`// Copyright (c) 2026 Second Star Cavalier LLC
// Generated service implementation example

package server

import (
	"context"

%s
	"go.uber.org/zap"
)

// ServiceImplementation implements the gRPC service interface
type ServiceImplementation struct {
%s}

// NewServiceImplementation creates a new service implementation
func NewServiceImplementation() *ServiceImplementation {
	return &ServiceImplementation{}
}

%s`, importBlock, embeds.String(), methods.String())

	// Format the generated code with gofmt
	formatted, err := format.Source([]byte(content))
	if err != nil {
		return fmt.Errorf("failed to format generated service.go: %w", err)
	}

	return os.WriteFile(filepath.Join(g.OutputDir, "internal", "server", "service.go"), formatted, 0644)
}

func (g *GoServerGenerator) generateInternalClient() error {
	header := header.GeneratedHeader(consts.LangGo, "")
	tmpl := header + `// This file contains a gRPC client that can connect to the server.

package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	pb "{{.TypesModule}}"
{{- if ne .TypesModule .ServicesModule}}
	svc "{{.ServicesModule}}"
{{- end}}
	fsconfig "{{.GrpcServerTypesModule}}"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/encoding/protojson"
)

// Client wraps the gRPC client connection and service clients
type Client struct {
	conn *grpc.ClientConn
{{range .ProtoInfo.Services}}	{{.Name}}Client {{svcAlias}}.{{.Name}}Client
{{end}}}

// NewClient creates a new client connection to the server
func NewClient(address string) (*Client, error) {
	// Connect to the server
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
	})))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %w", err)
	}

	client := &Client{
		conn: conn,
{{range .ProtoInfo.Services}}		{{.Name}}Client: {{svcAlias}}.New{{.Name}}Client(conn),
{{end}}	}

	return client, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Run executes the client example, testing all protocols
func Run() {
	// Load configuration
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml"
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config file: %v\n", err)
		os.Exit(1)
	}

	// Load grpcserver config
	config, err := fsconfig.ConfigFromBytes(configData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse config: %v\n", err)
		os.Exit(1)
	}

	// Determine addresses from config or environment
	grpcAddress := os.Getenv("SERVER_ADDRESS")
	restPort := 8443
	if grpcAddress == "" {
		if config.Grpc != nil {
			if config.Grpc.Port != 0 {
				grpcAddress = fmt.Sprintf("localhost:%d", config.Grpc.Port)
			}
		}
		if grpcAddress == "" {
			grpcAddress = "localhost:8443"
		}
	}
	if config.Rest != nil {
		if config.Rest.Port != 0 {
			restPort = int(config.Rest.Port)
		}
	}

	restBaseURL := fmt.Sprintf("http://localhost:%d/api/v1", restPort)
	grpcFailures := 0
	httpFailures := 0

	fmt.Println("=============================================================================")
	fmt.Println("Service Client Demo")
	fmt.Println("=============================================================================")
	fmt.Printf("gRPC server: %s\n", grpcAddress)
	fmt.Printf("REST server: %s\n", restBaseURL)
	fmt.Println()

	// === gRPC Tests ===
	fmt.Println("--- gRPC (binary protocol) ---")
	fmt.Println()

	fmt.Printf("Connecting to gRPC server at %s...\n", grpcAddress)
	client, err := NewClient(grpcAddress)
	if err != nil {
		fmt.Printf("✗ gRPC connection failed: %v\n", err)
		grpcFailures++
	} else {
		fmt.Println("✓ Connected")
		fmt.Println()

		ctx := context.Background()
{{range $service := .ProtoInfo.Services}}{{range $method := $service.Methods}}{{if and (eq $method.StreamingType "unary") (isLocalType $method.FullInputType) (isLocalType $method.FullOutputType)}}
		fmt.Println("Testing {{$service.Name}}.{{$method.Name}} via gRPC...")
		{
			req := &pb.{{$method.InputType}}{
{{generateFieldInitializers $method.InputType ""}}
			}
			fmt.Printf("Request: %s\n", protojson.Format(req))
			resp, err := client.{{$method.Name}}(ctx, req)
			if err != nil {
				fmt.Printf("✗ Failed: %v\n", err)
				grpcFailures++
			} else {
				fmt.Printf("Response: %s\n", protojson.Format(resp))
				fmt.Println("✓ Success")
			}
		}
{{end}}{{end}}{{end}}
		client.Close()
		fmt.Println()
		fmt.Println("gRPC connection closed")
	}
	fmt.Println()

	// === REST Tests ===
	fmt.Println("--- REST (HTTP/JSON) ---")
	fmt.Println()

	httpClient := &http.Client{Timeout: 5 * time.Second}
{{range $service := .ProtoInfo.Services}}{{range $method := $service.Methods}}{{if and (eq $method.StreamingType "unary") (isLocalType $method.FullInputType) (isLocalType $method.FullOutputType)}}
	fmt.Println("Testing {{$service.Name}}.{{$method.Name}} via REST...")
	if body, err := testREST(httpClient, restBaseURL, "{{toLower $service.Name}}", "{{toLower $method.Name}}", {{generateJSONBody $method.InputType}}); err != nil {
		fmt.Printf("✗ Failed: %v\n", err)
		httpFailures++
	} else {
		fmt.Printf("Response: %s\n", body)
		fmt.Println("✓ Success")
	}
{{end}}{{end}}{{end}}
	fmt.Println()
	fmt.Println("=============================================================================")
	if grpcFailures > 0 {
		fmt.Printf("Note: %d gRPC test(s) failed (server may not support gRPC)\n", grpcFailures)
	}
	if httpFailures > 0 {
		fmt.Printf("FAILED: %d REST test(s) failed\n", httpFailures)
		os.Exit(1)
	}
	fmt.Println("Demo complete: all tests passed")
	fmt.Println("=============================================================================")
}

func testREST(httpClient *http.Client, restBaseURL, service, method string, reqJSON []byte) (string, error) {
	url := fmt.Sprintf("%s/%s/%s", restBaseURL, service, method)
	fmt.Printf("POST %s\n", url)
	fmt.Printf("Request: %s\n", string(reqJSON))

	req, err := http.NewRequest("POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		return "", fmt.Errorf("request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

// Typed client methods
{{range $service := .ProtoInfo.Services}}{{range $method := $service.Methods}}{{if and (eq $method.StreamingType "unary") (isLocalType $method.FullInputType) (isLocalType $method.FullOutputType)}}
// {{$method.Name}} calls the {{$method.Name}} RPC method
func (c *Client) {{$method.Name}}(ctx context.Context, req *pb.{{$method.InputType}}) (*pb.{{$method.OutputType}}, error) {
	return c.{{$service.Name}}Client.{{$method.Name}}(ctx, req)
}
{{end}}{{end}}{{end}}
`

	funcMap := template.FuncMap{
		"toLower":                   strings.ToLower,
		"generateFieldInitializers": g.generateFieldInitializers,
		"generateJSONBody":          g.generateJSONBody,
		"svcAlias":                  func() string { return g.svcPrefix() },
		"isLocalType": func(fullTypeName string) bool {
			// FullInputType/FullOutputType are always qualified (e.g., "foobar.GetUserProfileRequest")
			// Cross-package types have a different package prefix (e.g., "baseball.GetRosterRequest")
			parts := strings.SplitN(fullTypeName, ".", 2)
			if len(parts) <= 1 {
				return true
			}
			return parts[0] == g.ProtoInfo.Package
		},
	}

	t, err := template.New("internal-client").Funcs(funcMap).Parse(tmpl)
	if err != nil {
		return err
	}

	// Execute template to buffer first
	var buf bytes.Buffer
	if err := t.Execute(&buf, g); err != nil {
		return err
	}

	// Format the generated code with gofmt
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format generated client.go: %w", err)
	}

	// Write formatted code to file
	return os.WriteFile(filepath.Join(g.OutputDir, "internal", "client", "client.go"), formatted, 0644)
}

// getMessageByName finds a message definition in this proto file.
// Package-qualified names from another proto are not folded to a local simple
// name; doing so can silently pick the wrong generated message.
func (g *GoServerGenerator) getMessageByName(name string) *parser.MessageInfo {
	if g == nil || g.ProtoInfo == nil {
		return nil
	}
	targetName := name
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		pkg := name[:idx]
		if pkg != "" && pkg != g.ProtoInfo.Package {
			return nil
		}
		targetName = name[idx+1:]
	}
	for _, msg := range g.ProtoInfo.Messages {
		if msg.Name == targetName {
			return msg
		}
	}
	return nil
}

// simpleTypeName extracts the simple type name from a potentially fully qualified name.
func simpleTypeName(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// generateExampleValue generates an example value for a field based on its type.
// Uses field name heuristics (Contains) for contextual placeholder values because
// proto has no annotation for example/default values in generated test scaffolding.
func (g *GoServerGenerator) generateExampleValue(field *parser.FieldInfo) string {
	switch field.Type {
	case "string":
		if strings.Contains(strings.ToLower(field.Name), "email") {
			return `"user@example.com"`
		} else if strings.Contains(strings.ToLower(field.Name), "id") {
			return `"example-id-123"`
		} else if strings.Contains(strings.ToLower(field.Name), "name") {
			return `"Example Name"`
		} else if strings.Contains(strings.ToLower(field.Name), "content") {
			return `"Example content"`
		}
		return `"example"`
	case "int32", "int64":
		return "123"
	case "bool":
		return "true"
	case "float", "double":
		return "1.23"
	case "bytes":
		return `[]byte("example")`
	default:
		if g.getMessageByName(field.Type) != nil {
			return "nested_message"
		}
		return ""
	}
}

// generateFieldInitializers generates Go struct field initializers for a protobuf message
func (g *GoServerGenerator) generateFieldInitializers(msgName string, indent string) string {
	msg := g.getMessageByName(msgName)
	if msg == nil {
		return ""
	}

	var b strings.Builder
	for i, field := range msg.Fields {
		exampleValue := g.generateExampleValue(field)
		if exampleValue == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n")
		}
		goFieldName := strutil.ToPascalCase(field.Name)
		if exampleValue == "nested_message" {
			nestedMsg := g.getMessageByName(field.Type)
			if nestedMsg != nil {
				goTypeName := simpleTypeName(field.Type)
				if field.Repeated {
					fmt.Fprintf(&b, "%s%s: []*pb.%s{{\n", indent, goFieldName, goTypeName)
					nestedFields := g.generateFieldInitializers(field.Type, indent+"")
					if nestedFields != "" {
						b.WriteString(nestedFields)
						b.WriteString("\n")
					}
					fmt.Fprintf(&b, "%s}},", indent)
				} else {
					fmt.Fprintf(&b, "%s%s: &pb.%s{\n", indent, goFieldName, goTypeName)
					nestedFields := g.generateFieldInitializers(field.Type, indent+"")
					if nestedFields != "" {
						b.WriteString(nestedFields)
						b.WriteString("\n")
					}
					fmt.Fprintf(&b, "%s},", indent)
				}
			}
		} else {
			if field.Repeated {
				fmt.Fprintf(&b, "%s%s: []%s{%s},", indent, goFieldName, field.Type, exampleValue)
			} else {
				fmt.Fprintf(&b, "%s%s: %s,", indent, goFieldName, exampleValue)
			}
		}
	}
	return b.String()
}

// generateJSONBody generates a JSON request body string for a protobuf message
func (g *GoServerGenerator) generateJSONBody(msgName string) string {
	msg := g.getMessageByName(msgName)
	if msg == nil {
		return `[]byte("{}")`
	}

	fields := g.buildJSONMap(msgName)
	if len(fields) == 0 {
		return `[]byte("{}")`
	}

	// Build a Go map literal
	var b strings.Builder
	b.WriteString("func() []byte { b, _ := json.Marshal(map[string]any{")
	for k, v := range fields {
		fmt.Fprintf(&b, `"%s": %s, `, k, formatGoLiteral(v))
	}
	b.WriteString("}); return b }()")
	return b.String()
}

func (g *GoServerGenerator) buildJSONMap(msgName string) map[string]any {
	msg := g.getMessageByName(msgName)
	if msg == nil {
		return nil
	}
	result := make(map[string]any)
	for _, field := range msg.Fields {
		val := g.generateExampleValue(field)
		if val == "" {
			continue
		}
		if val == "nested_message" {
			nested := g.buildJSONMap(field.Type)
			if field.Repeated {
				result[field.Name] = []any{nested}
			} else {
				result[field.Name] = nested
			}
		} else {
			switch field.Type {
			case "string":
				result[field.Name] = strings.Trim(val, `"`)
			case "int32", "int64":
				result[field.Name] = 123
			case "bool":
				result[field.Name] = true
			case "float", "double":
				result[field.Name] = 1.23
			default:
				result[field.Name] = val
			}
		}
	}
	return result
}

func formatGoLiteral(v any) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf(`"%s"`, val)
	case int:
		return fmt.Sprintf("%d", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case float64:
		return fmt.Sprintf("%g", val)
	case map[string]any:
		var b strings.Builder
		b.WriteString("map[string]any{")
		for k, v2 := range val {
			fmt.Fprintf(&b, `"%s": %s, `, k, formatGoLiteral(v2))
		}
		b.WriteString("}")
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteString("[]any{")
		for _, v2 := range val {
			fmt.Fprintf(&b, "%s, ", formatGoLiteral(v2))
		}
		b.WriteString("}")
		return b.String()
	default:
		return fmt.Sprintf("%v", val)
	}
}

func (g *GoServerGenerator) generateMethodStub(service *parser.ServiceInfo, method *parser.MethodInfo) string {
	// Skip methods with cross-package types (e.g., "baseball.GetRosterRequest" when package is "foobar")
	// The Unimplemented embed handles these methods with default responses
	isCrossPackage := func(fullType string) bool {
		parts := strings.SplitN(fullType, ".", 2)
		return len(parts) > 1 && parts[0] != g.ProtoInfo.Package
	}
	if isCrossPackage(method.FullInputType) || isCrossPackage(method.FullOutputType) {
		return fmt.Sprintf("// %s skipped (uses cross-package types)\n", method.Name)
	}

	sp := g.svcPrefix()

	switch method.StreamingType() {
	case "unary":
		return fmt.Sprintf(`// %s implements %s.%s
func (s *ServiceImplementation) %s(ctx context.Context, req *pb.%s) (*pb.%s, error) {
	zap.L().Info("Service method called",
		zap.String("service", "%s"),
		zap.String("method", "%s"))

	// TODO: Implement your business logic here
	// This is where you add your actual service implementation

	// For now, return a default response
	return &pb.%s{}, nil
}`, method.Name, service.Name, method.Name, method.Name, method.InputType, method.OutputType, service.Name, method.Name, method.OutputType)

	case "server-streaming":
		return fmt.Sprintf(`// %s implements %s.%s (server streaming)
func (s *ServiceImplementation) %s(req *pb.%s, stream %s.%s_%sServer) error {
	zap.L().Info("Service method called",
		zap.String("service", "%s"),
		zap.String("method", "%s"),
		zap.String("type", "server-streaming"))

	// TODO: Implement your streaming logic here
	// Send multiple responses back to the client

	// Example: Send a few responses
	for i := 0; i < 3; i++ {
		if err := stream.Send(&pb.%s{}); err != nil {
			return err
		}
	}

	return nil
}`, method.Name, service.Name, method.Name, method.Name, method.InputType, sp, service.Name, method.Name, service.Name, method.Name, method.OutputType)

	case "client-streaming":
		return fmt.Sprintf(`// %s implements %s.%s (client streaming)
func (s *ServiceImplementation) %s(stream %s.%s_%sServer) error {
	zap.L().Info("Service method called",
		zap.String("service", "%s"),
		zap.String("method", "%s"),
		zap.String("type", "client-streaming"))

	// TODO: Implement your streaming logic here
	// Receive multiple requests from the client and process them

	// Example: Receive all requests
	for {
		req, err := stream.Recv()
		if err != nil {
			break
		}
		// Process req
		_ = req
	}

	// Send final response
	return stream.SendAndClose(&pb.%s{})
}`, method.Name, service.Name, method.Name, method.Name, sp, service.Name, method.Name, service.Name, method.Name, method.OutputType)

	case "bidirectional":
		return fmt.Sprintf(`// %s implements %s.%s (bidirectional streaming)
func (s *ServiceImplementation) %s(stream %s.%s_%sServer) error {
	zap.L().Info("Service method called",
		zap.String("service", "%s"),
		zap.String("method", "%s"),
		zap.String("type", "bidirectional-streaming"))

	// TODO: Implement your bidirectional streaming logic here
	// Receive and send messages concurrently

	// Example: Echo back messages
	for {
		req, err := stream.Recv()
		if err != nil {
			break
		}
		// Process req
		_ = req

		// Send response
		if err := stream.Send(&pb.%s{}); err != nil {
			return err
		}
	}

	return nil
}`, method.Name, service.Name, method.Name, method.Name, sp, service.Name, method.Name, service.Name, method.Name, method.OutputType)

	default:
		return fmt.Sprintf("// TODO: Unknown streaming type for %s\n", method.Name)
	}
}

func (g *GoServerGenerator) generateReadme() error {
	tmpl := `# {{.Name}} Example Go Server

This example demonstrates how to implement a gRPC service directly in Go and automatically get REST support through the lib package.

## Architecture

` + "```" + `
┌─────────────────┐
│    main.go      │  Main entry point
└────────┬────────┘
         │
         ├─► service.go       Your gRPC service implementation
         │
         ├─► lib package       Auto-generated REST wrapper
         │
         └─► grpcserver           Server lifecycle management
` + "```" + `

## Key Concepts

1. **Implement gRPC Once**: Write your service logic by implementing the gRPC interface in ` + "`service.go`" + `
2. **Get REST Free**: The lib package automatically wraps your implementation to provide REST endpoints
3. **No State Binary**: Unlike proxy which spawns an external state service, this server implements the service directly in Go

## Services

{{range .ProtoInfo.Services}}### {{.Name}}

{{range .Methods}}- ` + "`{{.Name}}`" + ` ({{.InputType}}) → ({{.OutputType}}) [{{.StreamingType}}]
{{end}}
{{end}}

## Usage

### Build and Run

` + "```bash" + `
# Build the server
just build

# Run the server
just run

# Or run directly
CONFIG_PATH=config.yaml ./{{.Name}}-go-server
` + "```" + `

### Configuration

Edit ` + "`config.yaml`" + ` to configure:
- Listen address and port
- API prefix
- Logger settings
- OPA authentication (optional)
- Token cache settings

### Implementing Service Logic

Edit ` + "`service.go`" + ` to implement your business logic:

` + "```go" + `
func (s *ServiceImplementation) YourMethod(ctx context.Context, req *pb.Request) (*pb.Response, error) {
    // Add your implementation here
    return &pb.Response{
        // Fill in response fields
    }, nil
}
` + "```" + `

### Testing

Once the server is running, you can test it using:

**gRPC** (port 8443):
` + "```bash" + `
grpcurl -cacert ./ca.pem localhost:8443 {{.ProtoInfo.Package}}.ServiceName/MethodName
` + "```" + `

**REST** (https://localhost:8443/api/v1):
` + "```bash" + `
curl --cacert ./ca.pem -X POST https://localhost:8443/api/v1/servicename/methodname \
  -H "Content-Type: application/json" \
  -d '{"field": "value"}'
` + "```" + `

## Development

` + "```bash" + `
# Install dependencies
just deps

# Build
just build

# Run
just run

# Clean
just clean
` + "```" + `

## Comparison with Other Examples

- **proxy**: Proxies to external state process via stdio (use when state is in another language)
- **example-state**: Example state that runs over stdio (can be any language)
- **example-go-server** (this): Implements gRPC directly in Go, wrapped with lib for REST
- **example-client**: Client that can call any of the above using gRPC or REST
`

	t, err := template.New("readme").Parse(tmpl)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(g.OutputDir, "README.md"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, g)
}

func (g *GoServerGenerator) generateConfig() error {
	tmpl := `# Example Go Server Configuration

# Frontendserver configuration
proto_config:
  # Network binding address - used as base if protocol ports not specified
  listen_address: ":8443"

  # API prefix for REST endpoints
  # Example: "/api/v1" results in REST at "/api/v1/..."
  api_prefix: "/api/v1"

  # TLS configuration (required)
  # All protocols use the same port through ALPN negotiation.
  # tls:
  #   enabled: true
  #   tls_cert:
  #     cert_pem: |
  #       -----BEGIN CERTIFICATE-----
  #       ...
  #       -----END CERTIFICATE-----
  #     key_pem: |
  #       -----BEGIN PRIVATE KEY-----
  #       ...
  #       -----END PRIVATE KEY-----
  #   alpn_protocols:
  #     - ALPN_PROTOCOL_H2          # For gRPC
  #     - ALPN_PROTOCOL_HTTP_1_1    # For REST

  # gRPC protocol configuration
  grpc:
    enabled: true
    port: 8443

  # REST API configuration
  rest:
    enabled: true
    port: 8443

  # CORS configuration for REST (optional)
  # enable_cors: true
  # cors_origins:
  #   - "https://example.com"
  #   - "https://app.example.com"

  # Request limits
  max_request_size: 10485760      # 10MB in bytes
  request_timeout_seconds: 30     # Request timeout

  # Observability (optional)
  # enable_metrics: true   # Exposes /metrics endpoint
  # enable_tracing: true   # Enables OpenTelemetry tracing

  # Logger configuration
  logger:
    level: "info"
    encoding: "console"  # or "json"
    output_paths:
      - "stdout"
    error_output_paths:
      - "stderr"

  # Authorization rules (processed in key order, lowest first)
  auth_disabled: false
  auth_rules:
    1:
      action: ACTION_PERMIT
      method: "*"
      # issuer: "https://your-issuer.example.com"
  # auth_include_request: false

  # Disable automatic validation (optional, default: false)
  # Set to true only if validation is done elsewhere

  # Static file serving (optional)
  # Maps URL paths to filesystem directories
  # static_file_paths:
  #   "/static": "./public"
  #   "/assets": "/var/www/assets"

# Service-specific configuration
# Add your service-specific config fields here
service_config:
  # Example fields
  # database_url: "<driver>://host:5432/db"
  # cache_ttl: 3600
`

	return os.WriteFile(filepath.Join(g.OutputDir, "config.yaml"), []byte(tmpl), 0644)
}

func (g *GoServerGenerator) generateGoMod() error {
	// Get dependency registry
	registry, err := packages.GetPackageRegistry()
	if err != nil {
		return fmt.Errorf("failed to get dependency registry: %w", err)
	}

	// Get versions from registry
	zapVersion, ok := registry.GetGoVersion("go.uber.org/zap")
	if !ok {
		return fmt.Errorf("zap dependency not found in dummy/go.mod")
	}

	grpcVersion, ok := registry.GetGoVersion("google.golang.org/grpc")
	if !ok {
		return fmt.Errorf("grpc dependency not found in dummy/go.mod")
	}

	goVersion, ok := registry.GetToolVersion("go")
	if !ok {
		return fmt.Errorf("go tool version not found in dependencies.yaml")
	}

	header := header.SimpleHeader(consts.LangGo)
	tmpl := header + fmt.Sprintf(`module %s

go %s

require (
	go.uber.org/zap %s
	google.golang.org/grpc %s
)
`, g.GeneratedModule, goVersion, zapVersion, grpcVersion)

	return os.WriteFile(filepath.Join(g.OutputDir, "go.mod"), []byte(tmpl), 0644)
}
