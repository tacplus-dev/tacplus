#!/usr/bin/env bash
# Builds tacplus, generates the Go server model and the TypeScript client model
# from example/proto, builds and starts the Go server, runs the TypeScript
# client against it. Needs Go, Node and protoc (see CONTRIBUTING.md).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EX="$ROOT/example"
OUT="$EX/target"
TACPLUS_BIN="${TACPLUS_BIN:-$OUT/tacplus}"

rm -rf "$OUT"
mkdir -p "$OUT/go" "$OUT/ts"

if [[ "$TACPLUS_BIN" == "$OUT/tacplus" ]]; then
  echo "==> Building tacplus"
  (cd "$ROOT" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .)
fi

# pkg's last segment names the proto package this build HOSTS. The Go build
# hosts notesapi; the TypeScript build is a client of it, so it gets its own
# package name and hosts nothing.
write_config() {
  cat > "$1/tacplus.yaml" <<YAML
language: "$2"
pkg: "$3"
output-dir: "model"
header: |
  Copyright (c) 2026 Example Inc
  SPDX-License-Identifier: Apache-2.0
YAML
}

echo "==> Generating Go model"
write_config "$OUT/go" go com.example.notesapi
(cd "$OUT/go" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$EX/proto") > "$OUT/go/generate.log" 2>&1 || { cat "$OUT/go/generate.log"; exit 1; }
cat > "$OUT/go/go.mod" <<'MOD'
module example.com/notesapi

go 1.23
MOD

echo "==> Building Go server"
# example/server is its own module (so the root module's `go vet ./...` does
# not compile it); its go.mod points at the generated model in target/go.
(cd "$OUT/go" && go mod tidy) > "$OUT/go/tidy.log" 2>&1 || { cat "$OUT/go/tidy.log"; exit 1; }
(cd "$EX/server" && go mod tidy && go build -o "$OUT/notes-server" .) > "$OUT/go/server-build.log" 2>&1 || { cat "$OUT/go/server-build.log"; exit 1; }

echo "==> Generating TypeScript model"
write_config "$OUT/ts" typescript com.example.notesclient
(cd "$OUT/ts" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$EX/proto") > "$OUT/ts/generate.log" 2>&1 || { cat "$OUT/ts/generate.log"; exit 1; }
echo "==> Building TypeScript model"
(cd "$OUT/ts/model" && npm install && npm run build) > "$OUT/ts/npm-build.log" 2>&1 || { cat "$OUT/ts/npm-build.log"; exit 1; }
mkdir -p "$OUT/ts/client"
cp "$EX/client/client.mjs" "$OUT/ts/client/client.mjs"
ln -sfn "$OUT/ts/model/node_modules" "$OUT/ts/client/node_modules"

echo "==> Starting Go server"
"$OUT/notes-server" > "$OUT/server-stdout.log" 2> "$OUT/server-stderr.log" &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 100); do
  [[ -s "$OUT/server-stdout.log" ]] && break
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$OUT/server-stderr.log"; exit 1; }
  sleep 0.1
done
NOTES_ADDR="$(head -1 "$OUT/server-stdout.log")"
grep '^MCP tools' "$OUT/server-stderr.log" || true

echo "==> Running TypeScript client against $NOTES_ADDR"
(cd "$OUT/ts/client" && NOTES_ADDR="$NOTES_ADDR" node client.mjs)

echo "==> example OK"
