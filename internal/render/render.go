package render

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MasonXon/helm2yaml/internal/images"
	"github.com/MasonXon/helm2yaml/internal/verify"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/repo"
)

// commonFlags are the rendering options shared by the remote and local commands.
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
	Verify      bool     `help:"Verify the collected images (existence, linux/amd64+arm64, size sanity) instead of printing YAML."`
	VerifyMode  string   `name:"mode" enum:"exists,arch" default:"exists" help:"With --verify: exists (existence only) or arch (existence + linux/amd64/arm64)."`
	JSON        bool     `name:"json" help:"With --verify, output JSON instead of a table."`
	Stream      bool     `help:"With --verify, print results as each image completes."`
	Workers     int      `help:"With --verify, number of concurrent checks." default:"8"`
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

// Run downloads and renders a chart from a remote repository.
func (c *RemoteCmd) Run() error {
	tmp, err := os.MkdirTemp("", "helm2yaml-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	settings := cli.New()

	chartURL, err := repo.FindChartInAuthAndTLSAndPassRepoURL(
		c.Repo, c.Username, c.Password, c.Chart, c.Version,
		"", "", "", false, false, getter.All(settings),
	)
	if err != nil {
		return err
	}

	var opts []getter.Option
	if c.Username != "" {
		opts = append(opts, getter.WithBasicAuth(c.Username, c.Password))
	}
	dl := downloader.ChartDownloader{
		Out:              io.Discard,
		Getters:          getter.All(settings),
		Options:          opts,
		RepositoryConfig: settings.RepositoryConfig,
		RepositoryCache:  settings.RepositoryCache,
	}
	fname, _, err := dl.DownloadTo(chartURL, "", tmp)
	if err != nil {
		return fmt.Errorf("downloading chart from %q: %w", chartURL, err)
	}

	chrt, err := loader.LoadFile(fname)
	if err != nil {
		return err
	}

	return renderChart(chrt, c.commonFlags)
}

// Run loads and renders a local chart.
func (c *LocalCmd) Run() error {
	chrt, err := loader.Load(c.Path)
	if err != nil {
		return err
	}
	return renderChart(chrt, c.commonFlags)
}

// renderChart renders a loaded chart using the offline (client-only) engine and
// prints the resulting manifests, hooks, and optionally CRDs.
func renderChart(chrt *chart.Chart, f commonFlags) error {
	settings := cli.New()

	// The install action does not verify that the chart's dependencies are
	// vendored, so without this check a chart with missing subcharts would
	// render silently incomplete instead of failing.
	if chrt.Metadata != nil {
		if err := action.CheckDependencies(chrt, chrt.Metadata.Dependencies); err != nil {
			return err
		}
	}

	// Client-only mode means the install action never contacts a cluster; the
	// chart is rendered entirely offline by the Helm engine.
	cfg := &action.Configuration{
		RESTClientGetter: settings.RESTClientGetter(),
		Log:              func(string, ...interface{}) {},
	}

	valueOpts := values.Options{
		ValueFiles:   f.ValueFiles,
		Values:       f.SetValues,
		StringValues: f.SetString,
		FileValues:   f.SetFile,
		JSONValues:   f.SetJSON,
	}
	vals, err := valueOpts.MergeValues(getter.All(settings))
	if err != nil {
		return err
	}

	client := action.NewInstall(cfg)
	client.Namespace = f.Namespace
	client.ReleaseName = f.Name
	client.DryRun = true
	client.ClientOnly = true
	client.Replace = true
	client.IncludeCRDs = f.IncludeCRDs
	if f.KubeVersion != "" {
		kv, err := chartutil.ParseKubeVersion(f.KubeVersion)
		if err != nil {
			return fmt.Errorf("invalid --kube-version %q: %w", f.KubeVersion, err)
		}
		client.KubeVersion = kv
	}

	rel, err := client.Run(chrt, vals)
	if err != nil {
		return err
	}

	var out strings.Builder
	out.WriteString(strings.TrimSpace(rel.Manifest))
	if out.Len() > 0 {
		out.WriteString("\n")
	}
	for _, h := range rel.Hooks {
		if strings.TrimSpace(h.Manifest) == "" {
			continue
		}
		fmt.Fprintf(&out, "---\n# Source: %s\n%s\n", h.Path, h.Manifest)
	}

	if f.Verify {
		imgs, err := images.Extract(out.String(), f.ImagesAll)
		if err != nil {
			return err
		}
		return verify.Run(imgs, f.JSON, f.Stream, f.Workers, f.VerifyMode)
	}

	if f.Images || f.ImagesAll {
		imgs, err := images.Extract(out.String(), f.ImagesAll)
		if err != nil {
			return err
		}
		return writeOutput(images.FormatList(imgs), f.Output)
	}

	return writeOutput(out.String(), f.Output)
}

// writeOutput writes content to stdout or to a file when path is non-empty and
// not "-".
func writeOutput(content, path string) error {
	w := io.Writer(os.Stdout)
	if path != "" && path != "-" {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	_, err := io.WriteString(w, content)
	return err
}
