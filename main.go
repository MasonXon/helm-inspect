package main

import (
	"fmt"
	"os"

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
	Remote  RemoteCmd  `cmd:"" help:"Render a chart from a remote repository at a specific version."`
	Local   LocalCmd   `cmd:"" help:"Render a local chart directory or packaged .tgz file."`
	Version VersionCmd `cmd:"" help:"Print version."`
}

// commonFlags are the rendering options shared by both subcommands.
type commonFlags struct {
	ValueFiles  []string `short:"f" help:"Values YAML file (may be repeated, comma-separated, a URL, or '-' for stdin)."`
	SetValues   []string `name:"set" help:"Set values on the command line (key=val, comma-separated, repeatable)."`
	SetString   []string `name:"set-string" help:"Set STRING values on the command line."`
	SetFile     []string `name:"set-file" help:"Set values from files via key=path (repeatable)."`
	SetJSON     []string `name:"set-json" help:"Set values from JSON via key=json (repeatable)."`
	Namespace   string   `short:"n" help:"Kubernetes namespace used for rendering." default:"default"`
	Name        string   `help:"Release name used for rendering." default:"release-name"`
	KubeVersion string   `help:"Kubernetes version for capability checks, e.g. 1.29.3."`
	IncludeCRDs bool     `name:"include-crds" help:"Include CRDs from the chart's crds/ directory." default:"true" negatable:"true"`
	Images      bool     `help:"Print the deduplicated container image list instead of YAML."`
	ImagesAll   bool     `name:"images-all" help:"Like --images, but also scan ConfigMaps/annotations/env for image-like values."`
	Output      string   `short:"o" help:"Write output to this file (empty or '-' writes to stdout)."`
}

// RemoteCmd renders a chart pulled from a chart repository.
type RemoteCmd struct {
	commonFlags
	Repo     string `required:"" help:"Chart repository base URL, e.g. https://charts.bitnami.com/bitnami."`
	Chart    string `required:"" help:"Chart name inside the repository."`
	Version  string `required:"" help:"Chart version to render."`
	Username string `help:"Basic auth username for the repository."`
	Password string `help:"Basic auth password for the repository."`
}

// LocalCmd renders a chart from the local filesystem.
type LocalCmd struct {
	commonFlags
	Path string `arg:"" help:"Path to a chart directory or packaged .tgz file."`
}

// VersionCmd prints the build version.
type VersionCmd struct{}

// Run prints the build version.
func (VersionCmd) Run() error {
	fmt.Println(buildVersion)
	return nil
}
