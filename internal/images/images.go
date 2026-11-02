package images

import (
	"io"
	"regexp"
	"sort"
	"strings"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// imageRefRE matches a Docker/OCI image reference such as
// "repo", "repo:tag", "registry:5000/ns/repo:tag@sha256:<hex>".
var imageRefRE = regexp.MustCompile(`^(?:[a-zA-Z0-9][a-zA-Z0-9._-]*(?::[0-9]+)?/)?[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*(?::[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)

// Extract walks every YAML document in a rendered manifest and collects
// image references. When deep is true it also scans arbitrary config values
// (ConfigMaps, env, annotations, ...) for strings that look like image
// references, not just values under an "image" key.
func Extract(manifest string, deep bool) ([]string, error) {
	dec := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)

	seen := make(map[string]struct{})
	for {
		var obj interface{}
		err := dec.Decode(&obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		walkImages(obj, seen, deep)
	}

	images := make([]string, 0, len(seen))
	for img := range seen {
		images = append(images, img)
	}
	sort.Strings(images)
	return images, nil
}

func walkImages(v interface{}, out map[string]struct{}, deep bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			if s, ok := val.(string); ok {
				if k == "image" && s != "" {
					out[s] = struct{}{}
				} else if deep && looksLikeImage(k, s) {
					out[s] = struct{}{}
				}
			}
			walkImages(val, out, deep)
		}
	case []interface{}:
		for _, item := range t {
			walkImages(item, out, deep)
		}
	}
}

// looksLikeImage reports whether value (found under key) is plausibly a
// container image reference. In config values we require a registry/namespace
// path (a "/") plus a tag or digest, because bare "name:tag" is
// indistinguishable from "service:port" endpoints. Values under a key that
// clearly denotes an image (ends with "image") are accepted more leniently.
func looksLikeImage(key, value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	if strings.ContainsAny(value, " \t\r\n") || strings.Contains(value, "://") {
		return false
	}
	switch value {
	case "Always", "IfNotPresent", "Never", "true", "false":
		return false
	}
	if !imageRefRE.MatchString(value) || looksLikeHostPort(value) {
		return false
	}
	if strings.HasSuffix(normalizeKey(key), "image") {
		return true
	}
	return strings.Contains(value, "/") && (hasTag(value) || hasDigest(value))
}

// looksLikeHostPort rejects "host:port" style values (e.g. "192.168.1.1:8080",
// "db.internal:5432") that would otherwise look like "repo:tag".
func looksLikeHostPort(v string) bool {
	if strings.ContainsAny(v, "/@") {
		return false
	}
	i := strings.LastIndexByte(v, ':')
	if i < 0 {
		return false
	}
	host, port := v[:i], v[i+1:]
	if !isAllDigits(port) {
		return false
	}
	return strings.Contains(host, ".") || host == "localhost"
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasDigest(v string) bool { return strings.Contains(v, "@sha256:") }

// hasTag reports whether v has a tag, i.e. a ':' after the last '/'.
func hasTag(v string) bool {
	if i := strings.IndexByte(v, '@'); i >= 0 {
		v = v[:i]
	}
	return strings.LastIndexByte(v, ':') > strings.LastIndexByte(v, '/')
}

func normalizeKey(k string) string {
	k = strings.ToLower(k)
	k = strings.ReplaceAll(k, "_", "-")
	k = strings.ReplaceAll(k, ".", "-")
	return k
}

// defaultRegistry is used when no --default-registry is given: a reference
// without a registry host is a Docker Hub reference.
const defaultRegistry = "docker.io"

// ApplyDefaultRegistry prefixes references that do not name a registry with
// registry, e.g. "redis:7.8" -> "registry.example.com/ns/redis:7.8". When
// registry is empty it defaults to docker.io, and single-component references
// get Docker's implicit "library" namespace (nginx -> docker.io/library/nginx).
// References that already start with a registry host are returned unchanged.
func ApplyDefaultRegistry(refs []string, registry string) []string {
	registry = strings.TrimRight(strings.TrimSpace(registry), "/")
	if registry == "" {
		registry = defaultRegistry
	}
	out := make([]string, len(refs))
	for i, ref := range refs {
		if hasRegistryPrefix(ref) {
			out[i] = ref
			continue
		}
		if isDockerHub(registry) && !strings.Contains(ref, "/") {
			out[i] = registry + "/library/" + ref
			continue
		}
		out[i] = registry + "/" + ref
	}
	return out
}

// isDockerHub reports whether registry names Docker Hub, whose single-name
// images live under the implicit "library" namespace.
func isDockerHub(registry string) bool {
	switch registry {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return true
	}
	return false
}

// hasRegistryPrefix reports whether ref begins with an explicit registry host.
// It follows Docker's rule: with a path separator present, the first component
// is a registry when it contains "." or ":", is "localhost", or has uppercase.
func hasRegistryPrefix(ref string) bool {
	i := strings.IndexByte(ref, '/')
	if i < 0 {
		return false
	}
	first := ref[:i]
	return strings.ContainsAny(first, ".:") ||
		first == "localhost" ||
		first != strings.ToLower(first)
}

// FormatList renders one image per line, with a trailing newline when
// non-empty.
func FormatList(images []string) string {
	if len(images) == 0 {
		return ""
	}
	return strings.Join(images, "\n") + "\n"
}
