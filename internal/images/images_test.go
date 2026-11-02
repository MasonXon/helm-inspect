package images

import (
	"reflect"
	"testing"
)

func TestExtractImages(t *testing.T) {
	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata: {name: a}
spec:
  template:
    spec:
      initContainers:
        - name: init
          image: busybox:1.36
      containers:
        - name: app
          image: opencsghq/csghub-server:v2.6.1-ee
---
apiVersion: v1
kind: ConfigMap
metadata: {name: cfg}
data:
  CSGHUB_DATAFLOW_TEMPLATE_IMAGE: opencsghq/dataflow:v2.6.0-argo
  queue-sidecar-image: gcr.io/knative-releases/knative.dev/serving/cmd/queue@sha256:b1af8bda6c1d32b1cf5fbf8f1f6068c5007a5cebf091039fdea83b88b1fd87f4
  endpoint: 192.168.1.1:8080
  imagePullPolicy: Always
`
	got, err := Extract(manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"busybox:1.36",
		"gcr.io/knative-releases/knative.dev/serving/cmd/queue@sha256:b1af8bda6c1d32b1cf5fbf8f1f6068c5007a5cebf091039fdea83b88b1fd87f4",
		"opencsghq/csghub-server:v2.6.1-ee",
		"opencsghq/dataflow:v2.6.0-argo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractImages(deep) =\n%v\nwant\n%v", got, want)
	}

	// Strict mode only reports values under an "image" key.
	strict, err := Extract(manifest, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(strict) != 2 {
		t.Errorf("Extract(strict) = %v, want 2 container images", strict)
	}
}

func TestApplyDefaultRegistry(t *testing.T) {
	refs := []string{
		"redis:7.8",
		"opencsghq/csghub-server:v2.6.1-ee",
		"docker.io/library/nginx:1.25",
		"gcr.io/foo/bar@sha256:b1af8bda6c1d32b1cf5fbf8f1f6068c5007a5cebf091039fdea83b88b1fd87f4",
		"localhost:5000/app:1",
		"quay.io/argoproj/argocli:v3.7.11",
	}
	got := ApplyDefaultRegistry(refs, "registry.opencsg.com/opencsg/")
	want := []string{
		"registry.opencsg.com/opencsg/redis:7.8",
		"registry.opencsg.com/opencsg/opencsghq/csghub-server:v2.6.1-ee",
		"docker.io/library/nginx:1.25",
		"gcr.io/foo/bar@sha256:b1af8bda6c1d32b1cf5fbf8f1f6068c5007a5cebf091039fdea83b88b1fd87f4",
		"localhost:5000/app:1",
		"quay.io/argoproj/argocli:v3.7.11",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ApplyDefaultRegistry =\n%v\nwant\n%v", got, want)
	}

	// Without a registry, bare names default to Docker Hub; single-component
	// references get the implicit "library" namespace.
	got = ApplyDefaultRegistry([]string{"nginx:1.25", "opencsghq/foo:1", "quay.io/a/b:1"}, "")
	want = []string{"docker.io/library/nginx:1.25", "docker.io/opencsghq/foo:1", "quay.io/a/b:1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ApplyDefaultRegistry(default) = %v, want %v", got, want)
	}

	// An explicit Docker Hub registry behaves the same way.
	got = ApplyDefaultRegistry([]string{"nginx:1.25"}, "docker.io/")
	want = []string{"docker.io/library/nginx:1.25"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ApplyDefaultRegistry(docker.io) = %v, want %v", got, want)
	}
}

func TestHasRegistryPrefix(t *testing.T) {
	cases := map[string]bool{
		"redis:7.8":                  false,
		"opencsghq/dataflow:1":       false,
		"library/nginx:1":            false,
		"docker.io/library/nginx:1":  true,
		"localhost/app:1":            true,
		"localhost:5000/app:1":       true,
		"Registry.example.com/app:1": true,
	}
	for ref, want := range cases {
		if got := hasRegistryPrefix(ref); got != want {
			t.Errorf("hasRegistryPrefix(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestLooksLikeImage(t *testing.T) {
	cases := []struct {
		key, value string
		want       bool
	}{
		{"image", "nginx", true},
		{"image", "opencsghq/csghub-server:v2.6.1-ee", true},
		{"CSGHUB_DATAFLOW_TEMPLATE_IMAGE", "opencsghq/dataflow:v2.6.0-argo", true},
		{"queue-sidecar-image", "gcr.io/knative-releases/knative.dev/serving/cmd/queue@sha256:b1af8bda6c1d32b1cf5fbf8f1f6068c5007a5cebf091039fdea83b88b1fd87f4", true},
		{"somekey", "quay.io/argoproj/argocli:v3.7.11", true},
		{"somekey", "docker.io/opencsghq/git:2.52.0", true},
		{"image", "busybox:1.36", true},
		// false positives to reject
		{"apiVersion", "apps/v1", false},
		{"apiVersion", "admissionregistration.k8s.io/v1", false},
		{"label", "app.kubernetes.io/name", false},
		{"label", "eventing.knative.dev/release", false},
		{"path", "configs/sam2.1/sam2.1_hiera_l.yaml", false},
		{"imagePullPolicy", "Always", false},
		{"imagePullPolicy", "IfNotPresent", false},
		{"imageTag", "v2.6.0", false},
		{"endpoint", "192.168.1.1:8080", false},
		{"endpoint", "release-name-redis:6379", false},
		{"endpoint", "release-name-envoy-certgen:default", false},
		{"endpoint", "db.internal:5432", false},
		{"somekey", "postgres:15", false},
		{"endpoint", "release-name-envoy-knative.default.svc.cluster.local", false},
		{"url", "http://localhost:8001/api/v1/namespaces", false},
		{"path", "/var/run/secrets", false},
		{"registries", "csghub.example.com,docker.io", false},
		{"cron", "0 0 * * *", false},
	}
	for _, c := range cases {
		if got := looksLikeImage(c.key, c.value); got != c.want {
			t.Errorf("looksLikeImage(%q, %q) = %v, want %v", c.key, c.value, got, c.want)
		}
	}
}
