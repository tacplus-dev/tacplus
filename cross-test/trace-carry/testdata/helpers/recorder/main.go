// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The trace-helpers gate's recorder (tacplus#327): a plaintext gRPC server
// that answers every method, unary or streaming, and writes one line per
// request it receives, "<method> <trace_id>", the trace read off field 200 of
// the raw request. The Java and Rust legs point their generated clients at it.
//
// Every answer is the empty message, which is the default of any response
// type, except a record query's create: its handle carries query_id "q-1" (field
// 1 of query.QueryHandle), so the reader goes on to page and release it.
//
// Usage: recorder <listen address file> <log file>. It listens on 127.0.0.1
// on a free port and writes the address to the first file once it serves.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/encoding/protowire"
)

// rawCodec hands the server the request's bytes as they arrived.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) { return *(v.(*[]byte)), nil }
func (rawCodec) Unmarshal(data []byte, v any) error {
	*(v.(*[]byte)) = append([]byte(nil), data...)
	return nil
}
func (rawCodec) Name() string { return "proto" }

// traceID reads field 200 (a string) off a raw message, "" when it is unset.
func traceID(b []byte) (string, error) {
	trace := ""
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", protowire.ParseError(n)
		}
		b = b[n:]
		if num == 200 && typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return "", protowire.ParseError(m)
			}
			trace = string(v)
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return "", protowire.ParseError(m)
		}
		b = b[m:]
	}
	return trace, nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: recorder <address file> <log file>")
		os.Exit(2)
	}
	encoding.RegisterCodec(rawCodec{})
	logFile, err := os.Create(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var mu sync.Mutex
	record := func(method string, req []byte) error {
		trace, err := traceID(req)
		if err != nil {
			return fmt.Errorf("%s: request is not a protobuf message: %w", method, err)
		}
		mu.Lock()
		defer mu.Unlock()
		_, err = fmt.Fprintf(logFile, "%s %s\n", method[strings.LastIndex(method, "/")+1:], trace)
		if err == nil {
			err = logFile.Sync()
		}
		return err
	}
	handler := func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		name := method[strings.LastIndex(method, "/")+1:]
		for {
			var req []byte
			if err := stream.RecvMsg(&req); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			if err := record(method, req); err != nil {
				return err
			}
			reply := []byte{}
			if strings.HasPrefix(name, "Create") && strings.HasSuffix(name, "Query") {
				reply = protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "q-1")
			}
			if err := stream.SendMsg(&reply); err != nil {
				return err
			}
			// A server stream answers its one request and ends; a unary call
			// is done. Neither sends a second request.
			return nil
		}
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv := grpc.NewServer(grpc.UnknownServiceHandler(handler))
	if err := os.WriteFile(os.Args[1], []byte(lis.Addr().String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := srv.Serve(lis); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
