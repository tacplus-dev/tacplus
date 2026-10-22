// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The example server. It hosts the generated NotesApi wrapper on a plain gRPC
// server: the wrapper validates every request, runs the auth seams, and only
// then calls the handler below. In a deployed service the generated
// ServiceManager (s2sm.New + Run) does all of this wiring and also brings up
// identity, TLS and state; it needs the platform's identity service, so this
// standalone example wires the wrapper directly and verifies one demo token.
//
// Startup contract: prints the gRPC address on the first line of stdout, then
// serves until SIGINT/SIGTERM.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"

	"example.com/notesapi/model/chassis/collection"
	"example.com/notesapi/model/chassis/servicemanager"
	wrapperToken "example.com/notesapi/model/chassis/token"
	server "example.com/notesapi/model/s2sm/grpc/servers/notes/notesapi"
	types "example.com/notesapi/model/types/notes/notesapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// demoBearer is the only credential this example accepts. A real service
// verifies a signed token against the platform's keys; see chassis/grpcserver.
const demoBearer = "Bearer demo-token"

const (
	demoIssuer   = "https://issuer.example.com"
	demoSubject  = "spiffe://example.com/tenants/alpha/users/demo"
	demoAudience = "notes"
)

// demoToken is what the auth seams return on success: the wrapper builds the
// Caller it hands the handler from it.
var demoToken = mustDemoToken()

func mustDemoToken() *wrapperToken.TokenWrapper {
	part := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	jwt := part(map[string]string{"alg": "EdDSA", "kid": "demo"}) + "." +
		part(map[string]string{"iss": demoIssuer, "sub": demoSubject, "aud": demoAudience}) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("demo-signature"))
	tok, err := wrapperToken.ParseToken(jwt)
	if err != nil {
		panic(err)
	}
	return tok
}

func requireBearer(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if vals := md.Get("authorization"); len(vals) == 1 && vals[0] == demoBearer {
		return nil
	}
	return status.Error(codes.Unauthenticated, "missing or unknown bearer token")
}

// notes is the handler: the only code a service author writes.
type notes struct {
	mu    sync.Mutex
	next  int
	notes map[string]*types.Note // id -> note
}

func (n *notes) CreateNote(_ context.Context, caller *servicemanager.Caller, req *types.CreateNoteRequest) (*types.CreateNoteResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.next++
	note := &types.Note{
		Id:              fmt.Sprintf("note-%d", n.next),
		TenantNamespace: req.GetTenantNamespace(),
		Text:            req.GetText(),
	}
	n.notes[note.Id] = note
	fmt.Fprintf(os.Stderr, "CreateNote by %s: %s\n", caller.Token().GetSubject(), note.Id)
	return &types.CreateNoteResponse{Note: note}, nil
}

func (n *notes) GetNote(_ context.Context, _ *servicemanager.Caller, req *types.GetNoteRequest) (*types.GetNoteResponse, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	note, ok := n.notes[req.GetId()]
	if !ok || note.GetTenantNamespace() != req.GetTenantNamespace() {
		return nil, status.Errorf(codes.NotFound, "note %q not found", req.GetId())
	}
	return &types.GetNoteResponse{Note: note}, nil
}

// ListNotes is a PAGE collection handler: the wrapper decodes the page token
// into PageParams and encodes the cursor this returns into next_page_token.
func (n *notes) ListNotes(_ context.Context, _ *servicemanager.Caller, req *types.ListNotesRequest, page collection.PageParams) (collection.Page[*types.Note], error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var all []*types.Note
	for _, note := range n.notes {
		if note.GetTenantNamespace() == req.GetTenantNamespace() {
			all = append(all, note)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].GetId() < all[j].GetId() })
	start := 0
	if page.After != nil && len(page.After.Keys) == 1 {
		for start < len(all) && all[start].GetId() <= page.After.Keys[0] {
			start++
		}
	}
	end := min(start+page.Limit, len(all))
	out := collection.Page[*types.Note]{Items: all[start:end], Total: int64(len(all))}
	if end < len(all) {
		out.NextAfter = &collection.Cursor{Keys: []string{all[end-1].GetId()}}
	}
	return out, nil
}

func main() {
	handler := &notes{notes: map[string]*types.Note{}}
	wrapper := server.NewNotesApi(handler)

	// The auth seams. Each returns the token it verified; the wrapper turns it
	// into the Caller the handler receives.
	wrapper.SetAuthorizeFunc(func(ctx context.Context, _ string, _ any) (*wrapperToken.TokenWrapper, error) {
		if err := requireBearer(ctx); err != nil {
			return nil, err
		}
		return demoToken, nil
	})
	wrapper.SetAuthenticateFunc(func(ctx context.Context) (*wrapperToken.TokenWrapper, error) {
		if err := requireBearer(ctx); err != nil {
			return nil, err
		}
		return demoToken, nil
	})
	wrapper.SetAuthenticateAtOrBelowFunc(func(ctx context.Context, _ string) (*wrapperToken.TokenWrapper, error) {
		if err := requireBearer(ctx); err != nil {
			return nil, err
		}
		return demoToken, nil
	})

	grpcSrv := grpc.NewServer()
	if err := wrapper.RegisterServices(grpcSrv); err != nil {
		fmt.Fprintln(os.Stderr, "register:", err)
		os.Exit(1)
	}

	// The same rpcs, as the generated MCP tool catalogue.
	var tools []string
	for _, t := range server.NotesApiMcpTools() {
		tools = append(tools, t.Name)
	}
	fmt.Fprintf(os.Stderr, "MCP tools: %s\n", strings.Join(tools, ", "))

	addr := os.Getenv("NOTES_ADDR")
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	fmt.Println(lis.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		grpcSrv.GracefulStop()
	}()
	if err := grpcSrv.Serve(lis); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
