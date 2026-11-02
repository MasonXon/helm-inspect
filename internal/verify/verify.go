package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// imageCheckTimeout bounds all registry calls for a single image so one slow
// registry cannot stall the whole run.
const imageCheckTimeout = 30 * time.Second

// VerifyCmd verifies image references given as arguments, from a file, or from
// stdin.
type VerifyCmd struct {
	Images  []string `arg:"" optional:"" help:"Image references to verify."`
	File    string   `short:"f" help:"Read images from a file, one per line ('-' for stdin)."`
	Mode    string   `enum:"exists,arch" default:"exists" help:"exists: existence only; arch: existence + linux/amd64/arm64 (+ size sanity)."`
	Stream  bool     `help:"Print results as each image completes (NDJSON when combined with --json)."`
	JSON    bool     `name:"json" help:"Output results as JSON."`
	Workers int      `help:"Number of concurrent checks." default:"8"`
}

// Run verifies the gathered images.
func (c *VerifyCmd) Run() error {
	images, err := gatherImages(c.Images, c.File)
	if err != nil {
		return err
	}
	return Run(images, c.JSON, c.Stream, c.Workers, c.Mode)
}

// ImageCheck is the result of verifying a single image reference.
type ImageCheck struct {
	Image     string   `json:"image"`
	Exists    bool     `json:"exists"`
	NotFound  bool     `json:"notFound,omitempty"`
	Error     string   `json:"error,omitempty"`
	Platforms []string `json:"platforms,omitempty"`
	HasAMD64  bool     `json:"hasAmd64"`
	HasARM64  bool     `json:"hasArm64"`
	AMD64Size int64    `json:"amd64Size,omitempty"`
	ARM64Size int64    `json:"arm64Size,omitempty"`
	SizeEqual bool     `json:"sizeEqual,omitempty"`
	OK        bool     `json:"ok"`
}

// Verification modes.
const (
	modeExists = "exists" // existence only
	modeArch   = "arch"   // existence + linux/amd64/arm64 (+ size sanity)
)

// Run verifies images and prints a table or JSON, returning a non-nil
// error when any image fails the selected checks. When stream is true, results
// are printed as each image completes instead of after all checks finish.
func Run(images []string, asJSON, stream bool, workers int, mode string) error {
	if len(images) == 0 {
		return errors.New("no images to verify")
	}
	archMode := mode == modeArch

	var results []ImageCheck
	if stream {
		results = verifyStreaming(images, workers, mode, asJSON, archMode)
	} else {
		results = verifyImages(context.Background(), images, workers, mode, nil)
		if asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(results); err != nil {
				return err
			}
		} else {
			printVerifyTable(results, archMode)
		}
	}

	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d image(s) failed verification", failed, len(results))
	}
	return nil
}

// verifyStreaming prints each result as it completes: one table row, or one
// JSON object per line (NDJSON) when asJSON is set.
func verifyStreaming(images []string, workers int, mode string, asJSON, archMode bool) []ImageCheck {
	imageWidth := len("IMAGE")
	for _, img := range images {
		if len(img) > imageWidth {
			imageWidth = len(img)
		}
	}
	if !asJSON {
		streamTableHeader(imageWidth, archMode)
	}
	emit := func(r ImageCheck) {
		if asJSON {
			b, _ := json.Marshal(r)
			fmt.Fprintln(os.Stdout, string(b))
			return
		}
		streamTableRow(r, imageWidth, archMode)
		if r.Error != "" {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", r.Image, r.Error)
		}
	}
	return verifyImages(context.Background(), images, workers, mode, emit)
}

// verifyImages checks images concurrently while preserving input order. If
// emit is non-nil it is called (serialized) for each result as it completes.
func verifyImages(ctx context.Context, images []string, workers int, mode string, emit func(ImageCheck)) []ImageCheck {
	if workers < 1 {
		workers = 1
	}
	results := make([]ImageCheck, len(images))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var emitMu sync.Mutex
	for i, img := range images {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, img string) {
			defer wg.Done()
			defer func() { <-sem }()
			r := checkImage(ctx, img, mode)
			results[i] = r
			if emit != nil {
				emitMu.Lock()
				emit(r)
				emitMu.Unlock()
			}
		}(i, img)
	}
	wg.Wait()
	return results
}

func checkImage(parent context.Context, refStr, mode string) ImageCheck {
	res := ImageCheck{Image: refStr}

	ref, err := name.ParseReference(refStr)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	ctx, cancel := context.WithTimeout(parent, imageCheckTimeout)
	defer cancel()

	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithUserAgent("helm2yaml"),
	}

	if mode == modeExists {
		// Existence only: one HEAD request, no manifest body, no arch lookup.
		if err := withRetry(func() error {
			_, err := remote.Head(ref, opts...)
			return err
		}); err != nil {
			res.setFetchErr(err)
			return res
		}
		res.Exists = true
		res.OK = true
		return res
	}

	var desc *remote.Descriptor
	if err := withRetry(func() error {
		d, err := remote.Get(ref, opts...)
		if err != nil {
			return err
		}
		desc = d
		return nil
	}); err != nil {
		res.setFetchErr(err)
		return res
	}
	res.Exists = true

	platforms := map[string]struct{}{}

	if desc.MediaType.IsIndex() {
		// One manifest fetch already gives us every platform; sizes for the two
		// arches are fetched in parallel from their child manifests.
		idx, err := desc.ImageIndex()
		if err != nil {
			res.Error = err.Error()
			return res
		}
		im, err := idx.IndexManifest()
		if err != nil {
			res.Error = err.Error()
			return res
		}

		var amd64Digest, arm64Digest *v1.Hash
		for i := range im.Manifests {
			m := &im.Manifests[i]
			if m.Platform == nil {
				continue
			}
			p := m.Platform.OS + "/" + m.Platform.Architecture
			if m.Platform.Variant != "" {
				p += "/" + m.Platform.Variant
			}
			platforms[p] = struct{}{}
			if m.Platform.OS != "linux" {
				continue
			}
			d := m.Digest
			switch m.Platform.Architecture {
			case "amd64":
				if amd64Digest == nil {
					amd64Digest = &d
				}
			case "arm64":
				if arm64Digest == nil {
					arm64Digest = &d
				}
			}
		}

		var amd64Size, arm64Size int64
		var wg sync.WaitGroup
		if amd64Digest != nil {
			wg.Add(1)
			go func(d v1.Hash) { defer wg.Done(); amd64Size = childImageSize(idx, d) }(*amd64Digest)
		}
		if arm64Digest != nil {
			wg.Add(1)
			go func(d v1.Hash) { defer wg.Done(); arm64Size = childImageSize(idx, d) }(*arm64Digest)
		}
		wg.Wait()
		res.AMD64Size, res.ARM64Size = amd64Size, arm64Size
	} else {
		// Single-manifest image: size is derived from the manifest we already
		// have; only the tiny config blob is fetched to learn os/arch.
		m, err := v1.ParseManifest(bytes.NewReader(desc.Manifest))
		if err != nil {
			res.Error = err.Error()
			return res
		}
		size := m.Config.Size
		for _, l := range m.Layers {
			size += l.Size
		}
		img, err := desc.Image()
		if err != nil {
			res.Error = err.Error()
			return res
		}
		cfg, err := img.ConfigFile()
		if err != nil || cfg == nil {
			if err != nil {
				res.Error = err.Error()
			}
			return res
		}
		platforms[cfg.OS+"/"+cfg.Architecture] = struct{}{}
		switch cfg.Architecture {
		case "amd64":
			res.AMD64Size = size
		case "arm64":
			res.ARM64Size = size
		}
	}

	for p := range platforms {
		res.Platforms = append(res.Platforms, p)
	}
	sort.Strings(res.Platforms)
	res.HasAMD64 = hasPlatform(platforms, "linux/amd64")
	res.HasARM64 = hasPlatform(platforms, "linux/arm64")

	// amd64 and arm64 image sizes are practically never identical; equal sizes
	// usually indicate a mis-built image (e.g. one arch copied onto the other).
	if res.AMD64Size > 0 && res.AMD64Size == res.ARM64Size {
		res.SizeEqual = true
	}

	res.OK = res.Exists && res.HasAMD64 && res.HasARM64
	return res
}

// setFetchErr records a manifest/HEAD fetch error, distinguishing a clean 404
// (not found) from other failures.
func (r *ImageCheck) setFetchErr(err error) {
	var terr *transport.Error
	if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
		r.NotFound = true
		return
	}
	r.Error = err.Error()
}

// withRetry runs fn and retries it once when it fails with a transient
// network error.
func withRetry(fn func() error) error {
	err := fn()
	if err == nil || !isTransient(err) {
		return err
	}
	return fn()
}

// isTransient reports whether err is worth retrying: network timeouts,
// connection resets and registry-side 5xx/429 responses.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		return terr.StatusCode == http.StatusTooManyRequests || terr.StatusCode >= 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"TLS handshake timeout", "connection reset", "unexpected EOF", "i/o timeout", "no such host"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// imageSize returns the compressed storage size (config + layers) of an image.
func imageSize(img v1.Image) int64 {
	m, err := img.Manifest()
	if err != nil {
		return 0
	}
	total := m.Config.Size
	for _, l := range m.Layers {
		total += l.Size
	}
	return total
}

func childImageSize(idx v1.ImageIndex, digest v1.Hash) int64 {
	img, err := idx.Image(digest)
	if err != nil {
		return 0
	}
	return imageSize(img)
}

func hasPlatform(platforms map[string]struct{}, base string) bool {
	for p := range platforms {
		if p == base || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}

func printVerifyTable(results []ImageCheck, archMode bool) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if archMode {
		fmt.Fprintln(tw, "IMAGE\tEXISTS\tAMD64\tARM64\tAMD64 SIZE\tARM64 SIZE\tSTATUS")
		for _, r := range results {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Image,
				yesNo(r.Exists),
				yesNo(r.HasAMD64),
				yesNo(r.HasARM64),
				humanSize(r.AMD64Size),
				humanSize(r.ARM64Size),
				checkStatus(r, true),
			)
		}
	} else {
		fmt.Fprintln(tw, "IMAGE\tEXISTS\tSTATUS")
		for _, r := range results {
			fmt.Fprintf(tw, "%s\t%s\t%s\n",
				r.Image,
				yesNo(r.Exists),
				checkStatus(r, false),
			)
		}
	}
	tw.Flush()

	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", r.Image, r.Error)
		}
	}
}

func streamTableHeader(imageWidth int, archMode bool) {
	if archMode {
		fmt.Printf("%-*s  %-6s  %-5s  %-5s  %-12s  %-12s  %s\n",
			imageWidth, "IMAGE", "EXISTS", "AMD64", "ARM64", "AMD64 SIZE", "ARM64 SIZE", "STATUS")
	} else {
		fmt.Printf("%-*s  %-6s  %s\n", imageWidth, "IMAGE", "EXISTS", "STATUS")
	}
}

func streamTableRow(r ImageCheck, imageWidth int, archMode bool) {
	if archMode {
		fmt.Printf("%-*s  %-6s  %-5s  %-5s  %-12s  %-12s  %s\n",
			imageWidth,
			r.Image,
			yesNo(r.Exists),
			yesNo(r.HasAMD64),
			yesNo(r.HasARM64),
			humanSize(r.AMD64Size),
			humanSize(r.ARM64Size),
			checkStatus(r, true),
		)
	} else {
		fmt.Printf("%-*s  %-6s  %s\n", imageWidth, r.Image, yesNo(r.Exists), checkStatus(r, false))
	}
}

func checkStatus(r ImageCheck, archMode bool) string {
	var status string
	switch {
	case r.NotFound:
		status = "NOT FOUND"
	case r.Error != "":
		status = "ERROR"
	case archMode && (!r.HasAMD64 || !r.HasARM64):
		status = "MISSING ARCH"
	default:
		status = "ok"
	}
	if archMode && r.SizeEqual {
		status += " !SIZE-EQUAL"
	}
	return status
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func humanSize(n int64) string {
	if n <= 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// gatherImages collects image references from args, a file, and/or stdin,
// trimming blanks and comments and removing duplicates.
func gatherImages(args []string, file string) ([]string, error) {
	var raws []string
	raws = append(raws, args...)

	switch {
	case file == "-":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		raws = append(raws, strings.Split(string(data), "\n")...)
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		raws = append(raws, strings.Split(string(data), "\n")...)
	case len(args) == 0:
		if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice != 0 {
			return nil, errors.New("no images provided (pass image refs, -f file, or pipe a list on stdin)")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		raws = append(raws, strings.Split(string(data), "\n")...)
	}

	seen := make(map[string]struct{})
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		r = strings.TrimSpace(r)
		if r == "" || strings.HasPrefix(r, "#") {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out, nil
}
