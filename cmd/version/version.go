package version

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/buildinfo"
)

func GetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and build information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("tacplus version %s (commit %s)\n", buildinfo.Version, buildinfo.GitSha)
		},
	}
}
