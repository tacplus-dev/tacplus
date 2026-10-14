package spec

//go:generate mkdir -p ../../../../target/proto/spec
//go:generate protoc --proto_path=../../../proto --go_out=../../../../target/proto/spec --go_opt=paths=source_relative --go_opt=Msys/errors.proto=github.com/tacplus-dev/tacplus/target/proto/spec/sys;spec --go_opt=Msys/spec.proto=github.com/tacplus-dev/tacplus/target/proto/spec/sys;spec --go_opt=Msys/query.proto=github.com/tacplus-dev/tacplus/target/proto/spec/sys;spec --go_opt=Msys/tacplus.proto=github.com/tacplus-dev/tacplus/target/proto/spec/sys;spec ../../../proto/sys/errors.proto ../../../proto/sys/spec.proto ../../../proto/sys/query.proto ../../../proto/sys/tacplus.proto
