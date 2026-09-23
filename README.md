# caddy_custom

A custom Caddy distribution with additional upstream modules and two local HTTP
middlewares. It builds **Linux x64** and **Windows x64** executables. The included
Caddyfile is an example for hosting multiple PHP sites on Linux with PHP-FPM.

The project builds Caddy directly with Go. It does not use `xcaddy`, bundle
PHP-FPM, install a system service, or deploy to a server.

## Project layout

| Path | Purpose |
| --- | --- |
| `build.sh` | Downloads Go, resolves dependencies, and builds both executables. |
| `caddy/main.go` | Registers the included modules and starts the Caddy CLI. |
| `caddy/go.mod.initial` | Source of truth for direct dependencies. |
| `caddy/go.mod`, `caddy/go.sum` | Generated build inputs, ignored by Git. |
| `caddy_proxy_cache/` | Local disk-backed HTTP response cache. |
| `caddy_var_file/` | Local experimental middleware; currently incomplete. |
| `caddy/conf/Caddyfile` | The single checked-in example server configuration. |
| `.github/workflows/ci.yml` | Manually triggered build and release workflow. |
| `AGENTS.md` | Project conventions and instructions for coding agents. |
| `build/`, `bin/` | Downloaded toolchain and build outputs, ignored by Git. |

## Included components

| Component | Version | Purpose |
| --- | --- | --- |
| Go | `1.27.1` | Compiler downloaded by the build script. |
| Caddy | `v2.11.5-0.20260921221302-c18099a0af5e` | CLI and standard HTTP, TLS, file server, reverse proxy, and FastCGI modules. |
| `mholt/caddy-l4` | `v0.1.2` | Layer 4 TCP/UDP connection handling. |
| `caddyserver/forwardproxy` | `v0.0.0-20260321230143-0aab84dad4fc` | HTTP forward proxy. |
| `abiosoft/caddy-yaml` | `v0.0.0-20210522210701-64fbdd07cf02` | YAML configuration adapter. |
| `ducktype/caddy_proxy_cache` | Local source | `http.handlers.proxy_cache` middleware. |
| `ducktype/caddy_var_file` | Local source | `http.handlers.var_file` prototype. |

Caddy and the external plugins track the latest upstream default-branch commits
at update time, including unreleased changes. The branch heads checked on
2026-09-23 are:

| Repository | Branch | Commit |
| --- | --- | --- |
| Caddy | `master` | `c18099a0af5e` |
| caddy-l4 | `master` | `42db5690dea1` (also tagged `v0.1.2`) |
| forwardproxy | `master` | `0aab84dad4fc` |
| caddy-yaml | `master` | `64fbdd07cf02` |

The manifest records the Go version identifying each selected commit; it does
not query a moving branch on every build. A tag may appear when the branch head
is itself tagged. The older dates on some plugins reflect their current heads.

The local modules use relative `replace` directives. Their `v0.0.0` versions are
placeholders: the build uses the source directories in this repository.

Including a module in the executable does not enable it in the configuration.
The example uses `proxy_cache`; it does not configure forwardproxy, caddy-l4, or
`var_file`. YAML support remains included even though no YAML example is stored.

## Building

### Requirements

Run the build on **Linux x64**, including an x64 Linux environment in WSL, with
Bash, `wget`, `tar`, and network access to download Go and its modules. A system
Go installation is not required.

From the repository root:

```bash
bash ./build.sh
```

The script:

1. Downloads the declared Go version into `build/go<version>`, reusing it when
   the compiler already exists there.
2. Sets `CGO_ENABLED=0`, `GOARCH=amd64`, and `GOTOOLCHAIN=local`.
3. **Always copies `caddy/go.mod.initial` to `caddy/go.mod`.**
4. Runs `go mod tidy` to resolve indirect dependencies and generate `go.sum`.
5. Builds Linux and Windows with `-mod=readonly`, `-trimpath`, and
   `-ldflags="-s -w"`.
6. Runs `version` and `list-modules` on the Linux executable.

Outputs:

```text
bin/caddy-linux-amd64
bin/caddy-windows-amd64.exe
```

No ARM or 32-bit builds are produced. The build script uses a Linux toolchain;
it is not a native PowerShell build script. Windows is a supported output target.

### Dependency management

**Edit `caddy/go.mod.initial`, not the generated `caddy/go.mod`.** The initial
manifest contains only direct dependencies and the local module replacements.
Edits made only to the generated manifest are overwritten by the next build.

Go resolves indirect dependencies from the direct modules' manifests. They are
not manually listed in the initial manifest or all forced to `latest`.
Generated `go.mod` and `go.sum` files stay local and must not be committed.

To update components:

1. Check the current Go compiler release and the latest commits on Caddy's and
   each plugin's default branch (`master` or `main`), including unreleased commits.
2. Update `go_version` in `build.sh`, the `go` directive in `go.mod.initial` and
   both local modules, and the direct versions in `go.mod.initial`.
3. Rebuild both targets from the initial manifest and check the affected modules
   and configurations.
4. Update this README when versions or behavior change.

For Caddy and plugins, resolve the branch or exact commit with Go tooling, such
as `go list -m -json github.com/caddyserver/caddy/v2@master`. Do not substitute
`@latest`: it can select a stable tag older than the current branch head.

The generated `go.sum` checks downloaded module integrity, but it is not retained
in Git. This project intentionally does not keep a complete lock of indirect
dependencies across builds. Published executables contain Go build metadata;
inspect the actual compiled versions with `go version -m <executable>`.

## GitHub Actions and releases

The `ci` workflow runs only through **Actions → ci → Run workflow**, or:

```bash
gh workflow run ci.yml --ref main
```

A single `ubuntu-latest` job fetches the requested commit, runs `bash ./build.sh`,
and publishes both executables to a GitHub Release. The tag is the first
12 characters of the commit SHA.

The release step follows this simple pattern:

```bash
gh release create "$tag" ./bin/caddy-linux-amd64 ./bin/caddy-windows-amd64.exe --target "$GITHUB_SHA" ||
gh release upload "$tag" ./bin/caddy-linux-amd64 ./bin/caddy-windows-amd64.exe --clobber
```

If creation fails, the command attempts to upload and replace the assets of an
existing release with that tag. There is no separate release lookup. The job
uses GitHub's automatic token with `contents: write`; no personal token needs
to be stored in the repository. A failed build stops before publication.
The workflow publishes binaries, not a server deployment.

## Example configuration

`caddy/conf/Caddyfile` is the only checked-in configuration example. It is not
embedded in the executable or loaded during compilation. JSON and YAML copies
are not maintained; generate a representation when needed instead of keeping
parallel copies synchronized manually.

To inspect the adapted JSON without starting a server:

```bash
./bin/caddy-linux-amd64 adapt --config caddy/conf/Caddyfile --adapter caddyfile --pretty
```

`adapt` checks conversion, not the availability of credentials, PHP-FPM sockets,
or external services.

### Caddyfile behavior

- Listens on IPv4 HTTP/HTTPS ports, disables the admin API, and disables automatic
  HTTP-to-HTTPS redirects.
- Uses on-demand TLS with an authorization endpoint at
  `http://127.0.0.1:19000/caddy_on_demand_tls`. That service is external to this project.
- Chooses `root_name` by checking directories matching the full hostname, its
  last two labels, or its second-to-last label. The IPv4 host matcher selects
  `main`. The document root is `<roots_base>/<root_name>/public`.
- Uses separate matchers for these assignments, so multiple matches can overwrite
  earlier values. This is not Public Suffix List-aware domain selection.
- Serves static files, hides `*/.*`, removes the `Server` header, and configures
  gzip/zstd compression. Listed static extensions receive a one-year browser
  cache lifetime with `public,immutable`.
- Gives `/offline.html` precedence when that file exists.
- Applies Basic Auth when `/.htpasswd` exists. That file is only a presence check:
  credentials are defined in the configuration, not loaded from the file.
- Rewrites requests to PHP index files, places `proxy_cache` before the reverse
  proxy, and forwards PHP paths using FastCGI to
  `/run/php-fpm-<root_name>.sock`.

| Environment variable | Default |
| --- | --- |
| `CADDY_HTTP_PORT` | `80` |
| `CADDY_HTTPS_PORT` | `443` |
| `CADDY_ROOT_BASE` | `/var/www` |

The commented `CADDY_LOG_PATH` and bind variable examples are not active settings.
Debug logging is currently enabled in the example Caddyfile.

### Running on Linux

Prepare the site directories, PHP-FPM sockets, and TLS authorization service.
Replace `example-user` and `REPLACE_WITH_BCRYPT_HASH` locally, or remove the
Basic Auth block if it is not needed. The placeholder is not a valid bcrypt hash
and may prevent validation or startup even before a request matches that block.

Generate a password hash interactively:

```bash
./bin/caddy-linux-amd64 hash-password
```

After preparing the configuration:

```bash
./bin/caddy-linux-amd64 validate --config caddy/conf/Caddyfile --adapter caddyfile
./bin/caddy-linux-amd64 run --config caddy/conf/Caddyfile --adapter caddyfile
```

Use `--config <file.json>` for your own JSON configuration, or
`--config <file.yaml> --adapter yaml` for your own YAML configuration.

### Running on Windows

Use paths and services appropriate for Windows. The Linux PHP-FPM Unix socket
example is not directly portable. For a local HTTP smoke check, create a
`Caddyfile.windows` containing:

```caddyfile
http://localhost:8080 {
    respond "Caddy custom is running"
}
```

Then run in PowerShell:

```powershell
.\bin\caddy-windows-amd64.exe run --config .\Caddyfile.windows --adapter caddyfile
```

## Local module: proxy_cache

Registered as `http.handlers.proxy_cache`, with the Caddyfile directive
`proxy_cache`. It requires the HTTP `root` variable, which also separates cached
content by site. TTL, storage location, and cache policy currently have no
Caddyfile options; they are defined in the Go source.

### Request flow

1. Allows only `GET` and `HEAD`. Bypasses caching when `Authorization` or `Range`
   is present, or the `nocache` query parameter, `nocache` cookie, or `X-NoCache`
   header equals exactly `1`.
2. Builds a key from the document root, method, TLS presence, HTTP version, host,
   original URI including its query, and Authorization. Authorization is empty
   for requests that qualify for caching.
3. Looks under `caddy.AppDataDir()/proxy_cache/<md5-root>/<md5-key>`. MD5 is used
   for filenames, not encryption or data protection.
4. On a missing entry, waits for the next middleware's response and uses
   `singleflight` to coalesce updates of the same key.
5. On an expired entry, serves the existing copy while updating in the background.
   New responses are written to a `.tmp` file before replacing the cache entry.

| Source setting | Value |
| --- | --- |
| Response freshness | 300 seconds |
| Maximum wait for a missing entry | 300 seconds, then calls the next middleware directly |
| Cleanup interval | 30 seconds after each cleanup pass |
| File age eligible for cleanup | More than 60 minutes since modification |
| Cleanup limit | 200 candidate files per pass |

### Current limitations

- This is not a complete HTTP caching policy. Response status codes are not
  filtered, and `Cache-Control` or `Expires` do not determine cache eligibility.
- Cookies other than `nocache` do not separate cache keys, and `Vary` is not
  handled. Personalized content therefore requires an explicit bypass policy.
- `Cache-Control`, `Expires`, and `Set-Cookie` are omitted when replaying cached
  responses, but are still stored on disk with the other response headers.
- Cache files include response bodies, metadata, URLs, user agents, and the peer
  address/port (`RemoteAddr`). Debug logs also include Authorization values for
  requests that subsequently bypass the cache. Do not commit real cache or log data.
- I/O error handling and background operation lifecycle handling are basic.
  There are no dedicated integration tests at present.

## Local module: var_file

This is an **incomplete prototype**. Its JSON module ID is
`http.handlers.var_file`, but its Caddyfile directive is currently registered
as `vae_file`.

It does not read files: it sets `var_file.xxx` to the fixed string `aaaa` and
returns without calling the next middleware. Path parsing is also unfinished.
It is compiled into the executable but is not used by the example configuration.
Do not treat it as a working file-backed variable loader.

## Development checks

The full build checks compilation for both targets and runs the Linux CLI.
After building, check the local packages with:

```bash
./build/go1.27.1/bin/go -C caddy test . github.com/ducktype/caddy_proxy_cache github.com/ducktype/caddy_var_file
```

There are currently no `*_test.go` files. This command checks the packages but
is not a functional test suite. Middleware changes need additional checks for
real requests, cache misses/hits, bypasses, expiry, concurrency, and errors.

On Windows, check at least:

```powershell
.\bin\caddy-windows-amd64.exe version
.\bin\caddy-windows-amd64.exe list-modules
```

Successful compilation does not validate a destination server's PHP/TLS setup.
See `AGENTS.md` for repository editing conventions.
