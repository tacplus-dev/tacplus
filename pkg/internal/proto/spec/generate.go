package spec

//go:generate mkdir -p ../../../../target/proto/spec
//go:generate protoc --proto_path=../../../proto --go_out=../../../../target/proto/spec --go_opt=paths=source_relative --go_opt=Msys/errors.proto=gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys;spec --go_opt=Msys/spec.proto=gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys;spec --go_opt=Msys/query.proto=gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys;spec --go_opt=Msys/tacplus.proto=gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys;spec ../../../proto/sys/errors.proto ../../../proto/sys/spec.proto ../../../proto/sys/query.proto ../../../proto/sys/tacplus.proto
