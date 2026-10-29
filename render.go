package main

import (
	"fmt"
	"io"
	"os"
	"strings"

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

	if f.Images || f.ImagesAll {
		images, err := extractImages(out.String(), f.ImagesAll)
		if err != nil {
			return err
		}
		return writeOutput(formatImageList(images), f.Output)
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