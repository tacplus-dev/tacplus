// Standalone module boundary so the root module's `go test ./...` does NOT
// try to build this fixture in place. The fixture imports generated packages
// (gitlab.com/example/tacplus/consumer/contractclient/...) that exist only at
// gate runtime: cross-test/runtime-smoke/run.sh copies main.go into a temp dir
// alongside a freshly generated model and writes its own go.mod there (see
// write_fixture_mod). This committed go.mod is never built in place — it only
// carves the directory out of the root module. Mirrors write_fixture_mod.
module gitlab.com/example/tacplus/consumer/contractclient/runtime-fixture

go 1.23

require gitlab.com/example/tacplus/consumer/contractclient v0.0.0

replace gitlab.com/example/tacplus/consumer/contractclient => ..
