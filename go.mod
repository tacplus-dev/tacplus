module github.com/tacplus-dev/tacplus

go 1.25.0

require (
	github.com/spf13/cobra v1.10.2
	github.com/tacplus-dev/tacplus/templates v0.0.0-00010101000000-000000000000
	go.uber.org/zap v1.27.1
	golang.org/x/mod v0.33.0
	golang.org/x/tools v0.42.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

require (
	golang.org/x/sys v0.42.0
	google.golang.org/grpc v1.81.1
)

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
)

replace github.com/tacplus-dev/tacplus/templates => ./pkg/templates
