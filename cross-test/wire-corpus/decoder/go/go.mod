// Standalone module boundary so the root module's `go test ./...` does NOT try
// to build this decoder in place. It imports generated packages
// (gitlab.com/example/tacplus/corpus/corpusclient/...) that exist only at gate
// runtime: cross-test/wire-corpus/run.sh copies main.go into a temp dir
// alongside a freshly generated model and writes its own go.mod there (see
// run_go). This committed go.mod is never built in place — it only carves the
// directory out of the root module, and mirrors the one run_go writes.
module gitlab.com/example/tacplus/corpus/corpusclient/wire-corpus-decoder

go 1.23

require gitlab.com/example/tacplus/corpus/corpusclient v0.0.0

replace gitlab.com/example/tacplus/corpus/corpusclient => ..
