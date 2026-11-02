package main

import (
	"fmt"
	"os"

	"github.com/MasonXon/helm2yaml/internal/render"
	"github.com/MasonXon/helm2yaml/internal/verify"
	"github.com/alecthomas/kong"
)

var buildVersion = "dev"

func main() {
	var app CLI
	k := kong.Parse(&app,
		kong.Name("helm2yaml"),
		kong.Description("Render Helm charts to YAML from a remote repository at a given version or from a local chart."),
		kong.UsageOnError(),
	)
	if err := k.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// CLI is the root command set.
type CLI struct {
	Remote  render.RemoteCmd `cmd:"" help:"Render a chart from a remote repository at a specific version."`
	Local   render.LocalCmd  `cmd:"" help:"Render a local chart directory or packaged .tgz file."`
	Verify  verify.VerifyCmd `cmd:"" help:"Verify images exist and support linux/amd64 and linux/arm64."`
	Version VersionCmd       `cmd:"" help:"Print version."`
}

// VersionCmd prints the build version.
type VersionCmd struct{}

// Run prints the build version.
func (VersionCmd) Run() error {
	fmt.Println(buildVersion)
	return nil
}
