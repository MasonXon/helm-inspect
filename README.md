# helm2yaml

Render Helm charts to plain YAML — from a remote chart repository at a specific
version, or from a local chart directory / `.tgz` — and optionally collect and
verify every container image the chart deploys.

Rendering is fully offline: the chart is evaluated by the Helm engine in
client-only mode, so no Kubernetes cluster is contacted.

## Features

- Render a chart from any Helm repository at an exact version (basic auth supported).
- Render a local chart directory or a packaged `.tgz` archive.
- Full values handling: `-f/--value-files`, `--set`, `--set-string`, `--set-file`, `--set-json`.
- Includes CRDs (`--include-crds`, on by default) and chart hooks.
- Extracts the deduplicated container image list (`--images`), or a deeper scan
  that also catches image-like values in ConfigMaps, env vars, and annotations
  (`--images-all`).
- Verifies images against their registries: existence, `linux/amd64` +
  `linux/arm64` support, and a size-sanity heuristic for mis-built images.
- Table, JSON, or streamed NDJSON output for verification results.

## Install

```bash
# with Go 1.26+
go install github.com/MasonXon/helm-verify/cmd/helm2yaml@latest

# or build from source (host binary, upx-compressed where supported)
make build

# or cross-compile all platforms into dist/
make build-all
```

Prebuilt binaries are published on the
[releases page](https://github.com/MasonXon/helm-verify/releases).

## Use as a GitHub Action

The repository ships a composite action that installs the release binary and
runs it on the runner:

```yaml
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: MasonXon/helm-verify@v1.0.1
        with:
          args: local ./chart --verify --mode arch
```

`args` is passed to the CLI verbatim, so any subcommand works. The action
downloads the binary matching the ref it was called with and falls back to the
latest release for moving tags such as `@v1`; pin a full version tag to run
exactly that release.

## Usage

### Render a remote chart

```bash
helm2yaml remote \
  --repo https://charts.bitnami.com/bitnami \
  --chart nginx \
  --version 18.3.5 \
  -f values.yaml \
  --set replicaCount=2 \
  -n production \
  --name my-release \
  -o rendered.yaml
```

### Render a local chart

```bash
helm2yaml local ./charts/my-app -f values.yaml
helm2yaml local ./charts/my-app-0.1.0.tgz
```

Local charts must have their dependencies vendored under `charts/`
(run `helm dependency build` first). Rendering fails instead of silently
producing incomplete output when a declared dependency is missing.

### Collect images

```bash
# image fields only (e.g. containers[].image)
helm2yaml remote --repo ... --chart ... --version ... --images

# also scan ConfigMaps, annotations, and env for image-like values
helm2yaml remote --repo ... --chart ... --version ... --images-all
```

Output is one reference per line, sorted and deduplicated:

```
docker.io/opencsghq/csghub-server:v2.6.1-ee
quay.io/argoproj/argocli:v3.7.11
```

### Verify images

Verification can run directly on a list of references, or against the images a
chart renders.

```bash
# existence only (default)
helm2yaml verify docker.io/library/nginx:1.25 quay.io/argoproj/argocli:v3.7.11

# existence + linux/amd64/arm64 + size sanity
helm2yaml verify -f images.txt --mode arch

# verify what a chart deploys
helm2yaml remote --repo ... --chart ... --version ... --verify --mode arch

# stream results as each image completes, as NDJSON
helm2yaml verify -f images.txt --stream --json
```

Example table output:

```
IMAGE                             EXISTS  AMD64  ARM64  AMD64 SIZE  ARM64 SIZE  STATUS
quay.io/argoproj/argocli:v3.7.11  yes     yes    yes    68.9 MB     64.4 MB     ok
```

`--json` prints an array of result objects (or one object per line with
`--stream`):

```json
{
  "image": "quay.io/argoproj/argocli:v3.7.11",
  "exists": true,
  "platforms": ["linux/amd64", "linux/arm64"],
  "hasAmd64": true,
  "hasArm64": true,
  "amd64Size": 72289662,
  "arm64Size": 67526960,
  "ok": true
}
```

Verification only fetches manifests and config blobs, never layer data, so it
stays fast. Checks run concurrently (`--workers`, default 8) with a 30s budget
per image and one retry for transient network errors. Standard `HTTP_PROXY` /
`HTTPS_PROXY` / `NO_PROXY` environment variables are honored.

#### Modes and results

| Mode | Checks |
| --- | --- |
| `exists` (default) | The tag/digest resolves to a manifest. |
| `arch` | The above, plus `linux/amd64` and `linux/arm64` are present. |

`arch` mode also flags `SIZE-EQUAL`: if the amd64 and arm64 image sizes are
identical, the image was likely built incorrectly (one architecture copied onto
the other). This is a warning only and does not fail the run.

The command exits non-zero when any image is missing or (in `arch` mode) lacks a
required architecture. A 404 is reported as `NOT FOUND`, other failures as
`ERROR` with the underlying message on stderr.

## Shared render flags

| Flag | Description |
| --- | --- |
| `-f, --value-files` | Values YAML file; repeatable, comma-separated, a URL, or `-` for stdin. |
| `--set` | Set values (`key=val`), repeatable. |
| `--set-string` | Set string values, repeatable. |
| `--set-file` | Set values from files (`key=path`), repeatable. |
| `--set-json` | Set values from JSON (`key=json`), repeatable. |
| `-n, --namespace` | Namespace used for rendering (default `default`). |
| `--name` | Release name (default `release-name`). |
| `--kube-version` | Kubernetes version for capability checks, e.g. `1.29.3`. |
| `--include-crds` | Include CRDs from `crds/` (default true; disable with `--no-include-crds`). |
| `--images` | Print the image list instead of YAML. |
| `--images-all` | Like `--images`, plus a deep scan for image-like values. |
| `--verify` | Verify collected images instead of printing YAML. |
| `--mode` | Verification mode: `exists` or `arch`. |
| `--json` | JSON output for verification. |
| `--stream` | Print verification results as each image completes. |
| `--workers` | Concurrent verification checks (default 8). |
| `-o, --output` | Write output to a file (`-` or empty means stdout). |

## Build and test

```bash
make build      # host binary, version injected from git, upx-compressed
make build-all  # linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64
make test
make vet
```

Cross-built Linux and Windows binaries are compressed with
`upx --best --lzma`. macOS binaries are not packed: upx-compressed Mach-O
binaries are killed by the OS at startup, so the Makefile skips them.

## Project layout

| Path | Purpose |
| --- | --- |
| `cmd/helm2yaml/main.go` | CLI definition (kong) and version wiring. |
| `action.yml` | Composite GitHub Action that installs and runs the release binary. |
| `internal/render/` | Chart download/load, dependency check, offline rendering; the `remote` and `local` commands. |
| `internal/images/` | Image reference extraction and filtering from rendered YAML. |
| `internal/verify/` | Registry checks, concurrency, retries, and table/JSON output; the `verify` command. |
| `internal/*/*_test.go` | Unit and in-memory-registry integration tests. |
| `Makefile` | Build, cross-compile (upx), test, vet, format, clean. |

## License

[MIT](LICENSE)
