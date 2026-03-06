# open-mirror

A lazy-caching reverse proxy for Linux package repositories. When a package manager requests a file, open-mirror checks its local cache first. On a miss, it fetches from the upstream repository, caches the response, and serves it. Subsequent requests for the same file are served directly from cache.

Supports **APT** (Ubuntu, Debian), **Alpine APK**, and **Arch Linux** repositories out of the box. A single instance can proxy multiple distros simultaneously via URL prefix routing.

## Features

- **Lazy caching** -- files are cached on first access, no full mirror sync needed
- **Multi-distro** -- serve Ubuntu, Debian, Alpine, Arch (and more) from one instance
- **Smart TTL** -- package files (`.deb`, `.rpm`, `.apk`, etc.) are cached permanently; metadata (`InRelease`, `Packages.gz`, etc.) expires on a configurable TTL
- **By-Hash aware** -- content-addressed index files (`dists/*/by-hash/SHA256/...`) are correctly treated as immutable
- **Singleflight** -- concurrent requests for the same uncached file are coalesced into a single upstream fetch
- **Conditional requests** -- uses `If-Modified-Since` / `If-None-Match` when refreshing expired metadata; upstream 304 responses simply refresh the TTL
- **Atomic writes** -- cache files are written to a temp file first, then renamed
- **Zero external dependencies at runtime** -- single static binary, two Go libraries (`gopkg.in/yaml.v3`, `golang.org/x/sync`)

## Quick Start

### From source

```bash
go build -o open-mirror .
./open-mirror -config config.yaml
```

### With Docker

```bash
docker build -t open-mirror .
docker run -d \
  -p 8080:8080 \
  -v open-mirror-cache:/var/cache/open-mirror \
  open-mirror
```

### Cross-compile

Build scripts are included for common platforms:

```bash
./build-all.sh          # Linux amd64/arm64, macOS Intel/Apple Silicon, Windows amd64/arm64, FreeBSD amd64
./build-linux-amd64.sh  # Just Linux amd64
```

## Configuration

open-mirror reads a YAML config file (default: `config.yaml`). Example:

```yaml
listen: ":8080"
cache_dir: "./cache-data"

upstreams:
  - prefix: /ubuntu
    origin: http://archive.ubuntu.com/ubuntu
    metadata_patterns:
      - "*/InRelease"
      - "*/Release"
      - "*/Release.gpg"
      - "*/Packages*"
      - "*/Sources*"
      - "*/Translation-*"
      - "*/dep11/*"
      - "*/cnf/*"
      - "*/Contents-*"
    metadata_ttl: 3600s

  - prefix: /debian
    origin: http://deb.debian.org/debian
    metadata_patterns:
      - "*/InRelease"
      - "*/Release"
      - "*/Release.gpg"
      - "*/Packages*"
      - "*/Sources*"
      - "*/Translation-*"
      - "*/dep11/*"
      - "*/cnf/*"
      - "*/Contents-*"
    metadata_ttl: 3600s

  - prefix: /alpine
    origin: https://dl-cdn.alpinelinux.org/alpine
    metadata_patterns:
      - "*/APKINDEX.tar.gz"
    metadata_ttl: 3600s
```

### Config fields

| Field | Description | Default |
|---|---|---|
| `listen` | Address and port to listen on | `:8080` |
| `cache_dir` | Directory to store cached files | `./cache-data` |
| `upstreams[].prefix` | URL prefix to match (e.g. `/ubuntu`) | *required* |
| `upstreams[].origin` | Upstream repository URL | *required* |
| `upstreams[].metadata_patterns` | Glob patterns identifying metadata files | (fallback: white-list) |
| `upstreams[].metadata_ttl` | TTL for metadata before re-validation | `3600s` |

If `metadata_patterns` is omitted, a built-in white-list approach is used: known package file extensions (`.deb`, `.rpm`, `.apk`, `.pkg.tar.zst`, etc.) are treated as long-lived cache, and everything else is treated as metadata.

## Client Configuration

### Ubuntu / Debian (APT)

For Ubuntu 24.04+ using DEB822 format, edit `/etc/apt/sources.list.d/ubuntu.sources`:

```
Types: deb
URIs: http://<open-mirror-host>:8080/ubuntu
Suites: noble noble-updates noble-backports
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
```

For older systems using `sources.list`:

```
deb http://<open-mirror-host>:8080/ubuntu noble main restricted universe multiverse
deb http://<open-mirror-host>:8080/ubuntu noble-updates main restricted universe multiverse
```

### Alpine

Edit `/etc/apk/repositories`:

```
http://<open-mirror-host>:8080/alpine/v3.20/main
http://<open-mirror-host>:8080/alpine/v3.20/community
```

## CLI Flags

```
Usage of open-mirror:
  -config string
        path to config file (default "config.yaml")
  -v    enable verbose (debug) logging
  -version
        print version and exit
```

## Endpoints

| Path | Description |
|---|---|
| `/healthz` | Health check, returns `200 ok` |
| `/<prefix>/...` | Proxied/cached requests per configured upstreams |

Cached responses include the header `X-Cache: HIT`. Upstream-fetched responses include `X-Cache: MISS`.

## How It Works

1. A request arrives (e.g. `GET /ubuntu/dists/noble/InRelease`)
2. The handler matches the URL prefix to a configured upstream
3. The cache manager checks for a local copy:
   - **Package files** (`.deb`, etc.): cached permanently, always a hit if present
   - **Metadata files** (`InRelease`, `Packages.gz`, etc.): hit only if within TTL
4. On a **cache hit**: serve directly from disk with `X-Cache: HIT`
5. On a **cache miss**: fetch from upstream, stream the response to the client while simultaneously writing to cache, then store the metadata sidecar (`.meta.json`)
6. On a **TTL expiry** with existing cache: send a conditional request (`If-Modified-Since` / `If-None-Match`) to upstream; if 304, refresh TTL without re-downloading
7. Concurrent requests for the same uncached resource are coalesced via `singleflight`

## Development

```bash
# Run tests
go test -v ./...

# Build with version info
go build -ldflags="-X main.version=1.0.0 -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o open-mirror .
```

## License

[MIT](LICENSE)
