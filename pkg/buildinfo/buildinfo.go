package buildinfo

var (
	Version = "0.0.0"
	// GitSha is the VCS commit the binary was built from. CI stamps the real
	// commit hash; a local or unstamped build reads "local".
	GitSha      = "local"
	BuildID     = "local"
	ProjectPath = "seabeam/schematic/tacplus"
)
