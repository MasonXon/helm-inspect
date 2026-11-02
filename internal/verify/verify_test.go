package verify

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func startRegistry(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(s.Close)
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func pushIndex(t *testing.T, host, repo string, amd64Layers, arm64Layers int) string {
	t.Helper()
	amd, err := random.Image(1024, int64(amd64Layers))
	if err != nil {
		t.Fatal(err)
	}
	arm, err := random.Image(1024, int64(arm64Layers))
	if err != nil {
		t.Fatal(err)
	}
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	ref, err := name.NewTag(host + "/" + repo + ":latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	return ref.String()
}

func pushSingleArch(t *testing.T, host, repo, arch string) string {
	t.Helper()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cf = cf.DeepCopy()
	cf.OS = "linux"
	cf.Architecture = arch
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.NewTag(host + "/" + repo + ":latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	return ref.String()
}

func TestCheckImageMultiArch(t *testing.T) {
	host := startRegistry(t)
	ref := pushIndex(t, host, "app/multi", 2, 5)

	got := checkImage(context.Background(), ref, modeArch)
	if !got.Exists || !got.HasAMD64 || !got.HasARM64 || !got.OK {
		t.Fatalf("multi-arch check = %+v, want exists+amd64+arm64+ok", got)
	}
	if got.AMD64Size == 0 || got.ARM64Size == 0 {
		t.Errorf("sizes not populated: %+v", got)
	}
	if got.AMD64Size == got.ARM64Size {
		t.Errorf("expected different sizes for different layer counts, got %d", got.AMD64Size)
	}
	if got.SizeEqual {
		t.Errorf("SizeEqual should be false when sizes differ")
	}
}

func TestCheckImageSizeEqual(t *testing.T) {
	host := startRegistry(t)
	// A mis-built image: the very same manifest is tagged for both platforms,
	// so the amd64 and arm64 sizes are identical.
	img, err := random.Image(1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	ref, err := name.NewTag(host + "/app/equal:latest")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}

	got := checkImage(context.Background(), ref.String(), modeArch)
	if !got.OK {
		t.Fatalf("expected ok, got %+v", got)
	}
	if !got.SizeEqual {
		t.Errorf("SizeEqual = false, want true (suspicious equal sizes)")
	}
}

func TestCheckImageMissingArch(t *testing.T) {
	host := startRegistry(t)
	ref := pushSingleArch(t, host, "app/amdonly", "amd64")

	got := checkImage(context.Background(), ref, modeArch)
	if !got.Exists || !got.HasAMD64 || got.HasARM64 || got.OK {
		t.Fatalf("single-arch check = %+v, want exists+amd64, no arm64, not ok", got)
	}
}

func TestCheckImageNotFound(t *testing.T) {
	host := startRegistry(t)
	ref, err := name.NewTag(host + "/app/missing:1.0")
	if err != nil {
		t.Fatal(err)
	}

	got := checkImage(context.Background(), ref.String(), modeArch)
	if got.Exists || got.OK {
		t.Fatalf("missing image = %+v, want not exists", got)
	}
	if !got.NotFound {
		t.Errorf("NotFound = false, want true (got error %q)", got.Error)
	}
}

func TestCheckImageExistsMode(t *testing.T) {
	host := startRegistry(t)
	ref := pushSingleArch(t, host, "app/exists", "amd64")

	got := checkImage(context.Background(), ref, modeExists)
	if !got.Exists || !got.OK {
		t.Fatalf("exists mode = %+v, want exists+ok", got)
	}
	if got.HasAMD64 || got.HasARM64 {
		t.Errorf("exists mode should not populate arch info: %+v", got)
	}

	missing, err := name.NewTag(host + "/app/nope:1")
	if err != nil {
		t.Fatal(err)
	}
	got = checkImage(context.Background(), missing.String(), modeExists)
	if got.Exists || got.OK || !got.NotFound {
		t.Fatalf("exists mode missing = %+v, want not found", got)
	}
}

func TestVerifyImagesEmits(t *testing.T) {
	host := startRegistry(t)
	refs := []string{
		pushSingleArch(t, host, "app/a", "amd64"),
		pushIndex(t, host, "app/b", 1, 2),
	}

	var mu sync.Mutex
	emitted := map[string]bool{}
	results := verifyImages(context.Background(), refs, 4, modeArch, func(r ImageCheck) {
		mu.Lock()
		emitted[r.Image] = true
		mu.Unlock()
	})

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, ref := range refs {
		if !emitted[ref] {
			t.Errorf("result for %s was not emitted", ref)
		}
	}
}

func TestHasPlatform(t *testing.T) {
	ps := map[string]struct{}{"linux/amd64": {}, "linux/arm64/v8": {}, "windows/amd64": {}}
	cases := map[string]bool{
		"linux/amd64":   true,
		"linux/arm64":   true,
		"linux/arm/v7":  false,
		"windows/amd64": true,
	}
	for base, want := range cases {
		if got := hasPlatform(ps, base); got != want {
			t.Errorf("hasPlatform(%q) = %v, want %v", base, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:               "-",
		512:             "512 B",
		1024:            "1.0 KB",
		1024 * 1024:     "1.0 MB",
		3 * 1024 * 1024: "3.0 MB",
	}
	for n, want := range cases {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCheckStatus(t *testing.T) {
	cases := []struct {
		c    ImageCheck
		want string
	}{
		{ImageCheck{NotFound: true}, "NOT FOUND"},
		{ImageCheck{Error: "boom"}, "ERROR"},
		{ImageCheck{Exists: true, HasAMD64: true, HasARM64: false}, "MISSING ARCH"},
		{ImageCheck{Exists: true, HasAMD64: true, HasARM64: true}, "ok"},
		{ImageCheck{Exists: true, HasAMD64: true, HasARM64: true, SizeEqual: true}, "ok !SIZE-EQUAL"},
	}
	for _, tc := range cases {
		if got := checkStatus(tc.c, true); got != tc.want {
			t.Errorf("checkStatus(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
	// In exists mode a bare existing image is "ok" even without arch info.
	if got := checkStatus(ImageCheck{Exists: true}, false); got != "ok" {
		t.Errorf("checkStatus(exists mode) = %q, want ok", got)
	}
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("unexpected EOF"), true},
		{errors.New("dial tcp: lookup quay.io: no such host"), true},
		{errors.New("boom"), false},
		{&transport.Error{StatusCode: 503}, true},
		{&transport.Error{StatusCode: 429}, true},
		{&transport.Error{StatusCode: 404}, false},
	}
	for _, tc := range cases {
		if got := isTransient(tc.err); got != tc.want {
			t.Errorf("isTransient(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestWithRetry(t *testing.T) {
	calls := 0
	err := withRetry(func() error {
		calls++
		if calls == 1 {
			return errors.New("unexpected EOF")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withRetry returned %v, want nil", err)
	}
	if calls != 2 {
		t.Errorf("withRetry made %d calls, want 2", calls)
	}

	calls = 0
	wantErr := errors.New("permanent")
	err = withRetry(func() error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("withRetry returned %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Errorf("withRetry made %d calls for a non-transient error, want 1", calls)
	}
}

func TestGatherImages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "images.txt")
	content := "a/b:1\n\n# comment\nc/d:2\na/b:1\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := gatherImages([]string{"x/y:3"}, path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"x/y:3", "a/b:1", "c/d:2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gatherImages = %v, want %v", got, want)
	}
}
