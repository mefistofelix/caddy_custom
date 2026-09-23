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
content by site. The implementation remains in `caddy_proxy_cache/proxy_cache.go`.

### Response policy and configuration

```caddyfile
proxy_cache {
    methods GET HEAD
    storage_path /var/cache/caddy
    valid 200 404 301 302 303 307 5m
    ignore_headers Cache-Control Expires Set-Cookie
    bypass `!({http.request.cookie.nocache} in ["", "0"]) || !({http.request.header.X-NoCache} in ["", "0"]) || !({http.request.uri.query.nocache} in ["", "0"]) || {http.request.header.Authorization} != ""`
    inactive 720h
    max_age 2160h
    wait_timeout 600s
}
```

This example uses the earlier Nginx-style response policy, thirty-day inactivity
retention, and a ninety-day absolute cap. Bare `proxy_cache` retains the defaults
below; these settings are not all required.

| Option | Default | Meaning |
| --- | --- | --- |
| `methods METHOD ...` | `GET HEAD` | Exact, case-sensitive allowlist; replaces the default list. |
| `storage_path path` | `caddy.AppDataDir()/proxy_cache` | Dedicated cache directory, supporting request placeholders such as `{http.request.host}` and `{http.vars.root_name}`. Global placeholders such as `{env.CACHE_DIR}` resolve at provisioning; request placeholders resolve before cache lookup. Relative paths resolve from the process working directory. Temporary files stay beside the final entry for rename. |
| `key template` | Built-in key described below | Request-time Caddy placeholder template, replacing the default key. |
| `bypass expression` | Authorization present or any implemented `nocache` signal exactly `1` | Caddy CEL request matcher, compiled at provisioning. `true` skips both reading and writing the cache. An explicit expression replaces this default bypass policy. |
| `inactive duration` | `0` (disabled) | Retention limit since the last cache access, tracked using file modification time. |
| `max_age duration` | `1h` | Absolute retention limit since response capture started, independent of accesses; `0` disables it. |
| `wait_timeout duration` | `5m` | Positive maximum wait for a missing entry, also used as the background refresh timeout. |

Both retention limits are checked on reads and by the cleaner. When either
enabled limit expires, the entry cannot be served, even stale. A successful
refresh creates a new entry and starts its retention clocks again. Only an
enabled `inactive` causes cache reads to update mtime. Creation time and policy
durations are stored in the file, so touching it cannot extend `max_age`, change
response freshness, or reset `Age`. Entries sharing a storage directory retain
their own stored limits when another middleware instance runs cleanup.

For storage separated by application and virtual host:

```caddyfile
storage_path "/var/cache/caddy/{vars.root_name}/{host}"
```

Set `root_name` before `proxy_cache`, as in the checked-in Caddyfile. A variable
may also contain an absolute directory: `storage_path "{vars.cache_dir}"`.
Unknown or empty path placeholders fail the request before accessing the cache.
Use trusted path variables; values are filesystem paths, not sanitized identifiers.
The cleaner tracks resolved directories concurrently. After a reload or restart,
a dynamic directory is registered again on its first eligible request; dormant
directories are not swept until then. Retention is always checked on lookup.
The document-root namespace remains, and singleflight uses the complete resolved
cache path, so equal keys in different storage directories do not share a fill.

For example, an explicit key can use:

```caddyfile
key "{http.request.scheme}|{http.request.method}|{http.request.host}|{http.request.orig_uri}|{http.request.header.Cookie}"
```

An explicit key controls the request dimensions: automatic header and body
digests are no longer appended. Include relevant cookie, authorization, and body
dimensions unless covered by the upstream's `Vary` response header or bypass
policy. `Vary` dimensions are discovered automatically as described below.
`{http.request.body_hash}` provides the MD5
digest of the buffered request body for key templates. The document-root
namespace and response/retention policy fingerprint remain separate guards.
Unknown key placeholders cause an error rather than silently collapsing keys.
Caddyfile shorthand placeholders are expanded by Caddy's adapter; JSON uses
their full names.

CEL supports Caddy's request placeholders and matcher functions. Quote the entire
expression with backticks, or use ordinary unquoted CEL syntax. Configuration
errors fail provisioning; runtime evaluation errors return an error without
serving or populating the cache. For example, `bypass false` disables the default
Authorization/nocache bypass, so the key must then separate authenticated users.
Range and Upgrade requests always bypass independently of CEL.

The configuration borrows the relevant semantics of Nginx's
[`proxy_cache_valid`](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_cache_valid)
and [`proxy_ignore_headers`](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_ignore_headers):

- `valid [status ...|any] duration` selects eligible statuses and their fallback
  TTL. Repeat it for different statuses; an exact status takes precedence over
  `any`. With no statuses, it applies to 200, 301, and 302. A zero duration disables
  that status. Once `valid` is configured, unlisted statuses are not cached unless
  covered by `any`.
- Bare `proxy_cache` caches statuses 200, 301, 302, 303, 307, 308, and 404 for up to
  five minutes, subject to the response headers below.
- `ignore_headers Header ...` disables processing of those response headers for
  cache policy. It does **not** remove headers from the client response or disk.
  Relevant names are `Cache-Control`, `Expires`, `Set-Cookie`, and `Vary`.
  Nothing is ignored by default. In particular, ignoring `Set-Cookie` explicitly
  permits storing and replaying cookies; ignoring `Vary` disables automatic
  variant selection and permits `Vary: *`.

The decision is made when the final response status and headers are available.
`private`, `no-cache`, or `no-store`, any `Set-Cookie`, and `Vary: *` prevent
publication. For eligible statuses, `s-maxage` takes precedence over `max-age`,
then `Expires`, then the configured fallback TTL. Invalid or expired freshness
values prevent caching. `Age` and `Date` reduce the remaining max-age lifetime.
Response headers are preserved on both the initial response and cache hits;
cached responses also account for time on disk in `Age`.

### Automatic Vary variants

The file at the base key contains a complete response and its `Vary` metadata;
there is no separate index file. Every lookup starts there. If the request's
variant hash matches, that response is used. Otherwise the cache opens a secondary
file named by a hash of the base key and the header names and request values
selected by `Vary`. Multiple `Vary` fields are combined; header names are matched
case-insensitively. Spaces and list separators in `Accept-Charset`,
`Accept-Encoding`, and `Accept-Language` are normalized, following Nginx's approach.
Other header values are combined in order without semantic normalization.

For example, a custom base key can omit `Accept-Language`: a response with
`Vary: Accept-Language` then separates Italian and English automatically while
irrelevant request headers do not fragment that custom key. The default key
still includes all headers and remains deliberately more conservative.

On a cold base key, requests with that same key wait for its first fill. Each
waiter then repeats lookup and checks its own variant; it cannot reuse another
language's response. Once discovered, distinct variants fill and refresh
independently, with immediate stale serving for the matching variant. The fill
uses a snapshot of the incoming headers, before downstream handlers can mutate
them. Uncacheable responses are never shared.

If a secondary response changes or removes `Vary`, its completed response replaces
the primary file, establishing the new selection rule. Failed fills do not change
that rule. Metadata survives restarts. If the primary file is removed, discovery
requires another upstream response even when secondary files remain; ordinary
retention cleanup eventually removes unused variants. This follows Nginx's storage
pattern, not its binary format or every HTTP normalization rule.

### Request flow

1. Checks the method allowlist, the bypass policy, Range, and Upgrade. Request
   bodies for eligible requests are buffered in memory and given independent
   readers for the fill and any timeout fallback; configured methods such as
   POST can therefore be cached without consuming another request's body.
2. Builds a key from the document root, method, TLS presence, HTTP version, host,
   original URI including its query, and digests of all request headers and the
   body, unless an explicit `key` replaces these dimensions. By default, cookies
   and all header-based `Vary` dimensions are therefore separated **before**
   singleflight, including on the first miss. This deliberately separates more
   variants than `Vary` requires; changing irrelevant headers also reduces hits.
   The configured policy is included so changing overrides does not reuse entries
   written under the previous policy.
3. Looks under `<storage_path>/<md5-root>/<md5-key>`, then follows stored `Vary`
   metadata to a secondary variant when needed. MD5 is used for filenames, not
   encryption or data protection. A variant is checked before serving it stale.
4. On a missing entry, the initiating request receives headers and body as the
   upstream produces them. Cacheable body chunks are also written to the private
   temporary file. `singleflight` coalesces updates of that exact cache path:
   waiters receive only a completed, published response, after checking their own
   `Vary` dimensions. Other keys run independently. An uncacheable response belongs
   only to its initiating request; waiters obtain their own upstream responses.
5. On an expired entry, serves the existing copy while updating in the background.
   The refresh has its own request variables and a context tied to the module,
   so completing the original request does not cancel it. Concurrent requests
   keep receiving the previous copy while one refresh runs for their exact key.
6. Keeps response headers in memory until the final status, then decides where
   the body goes. A cacheable response creates a unique temporary file and is
   published only after a successful complete body and file close, using rename.
   All miss responses stream to their initiating request through an unbuffered
   pipe, with headers and body chunks flushed to the client. Header-rejected
   responses create no file; neither path buffers the whole body in memory.
   The request goroutine alone writes to its client; timeout or cancellation closes
   the pipe so an abandoned producer cannot write to that client later. Waiting
   requests obtain their own upstream responses, including if the initiating stream
   fails. Background refreshes discard uncacheable bodies and remove the previous
   entry only after successful completion. Incomplete responses and failed
   refreshes do not replace an existing entry.

Cache metadata includes response expiry, creation time, both retention
durations, `Vary`, and a variant hash. Older-format entries are refilled. A response
accepted at headers can still exceed `max_age` during capture: the initiating
client receives its stream, but the temporary file is removed without publication.
Abandoned captures are cleaned up when the producer finishes. After headers have
been sent, a failed upstream or capture can leave the initiating client with a
partial response; that response is never published for waiters.

This matches Nginx's basic miss behavior with `proxy_cache_lock on`: the first
client streams while equal-key waiters await publication. It is not a broadcast
of an unfinished body to all waiters. The small implementation uses synchronous
disk writes and an unbuffered pipe: a slow initiating client can slow its fill and
same-key waiters. It does not reproduce Nginx's separate upstream/client buffering.

| Source setting | Value |
| --- | --- |
| Default response freshness | 300 seconds; configurable and overridden by response freshness headers |
| Maximum wait for a missing entry | `wait_timeout`, then calls the next middleware directly without caching that fallback. Once the initiating response starts streaming, its lifetime follows the request context instead. |
| Background refresh timeout | `wait_timeout` |
| Cleanup interval | 30 seconds; stops when the module is cleaned up |
| File age eligible for cleanup | Either enabled retention limit has expired; abandoned temporary files older than one hour are also removed |
| Cleanup limit | 200 candidate files per pass |

### Current limitations

- This remains a small response cache, not a complete RFC cache or an Nginx
  reimplementation. There is no conditional revalidation or purge API. Existing stale
  serving remains enabled; `must-revalidate` and request cache directives do not
  change that behavior.
- Personalization using information outside the key, such as the peer address,
  still needs an explicit bypass. Matching cookies alone do not guarantee that
  content is suitable for caching; the upstream should emit the appropriate
  response policy.
- Cleanup and filesystem failure recovery remain basic. A storage directory
  should be dedicated to cache files. Request-body buffering consumes memory
  proportional to the body size.
- Cache files include response bodies, metadata, URLs, user agents, and the peer
  address/port (`RemoteAddr`). If an explicit bypass policy permits authenticated
  requests, debug logs and default-key metadata can include Authorization values.
  Do not commit real cache or log data.

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

`caddy_proxy_cache/proxy_cache_test.go` exercises response policy, configuration,
misses/hits, independent cookie and Vary variants, concurrent fills, private
responses, stale refresh, CEL bypass, custom keys and storage, request bodies,
retention clocks, wait timeouts, and incomplete upstream responses through
in-process HTTP handlers. The other local module has no tests.

For cache concurrency changes, use the race detector on a host with a C compiler:

```bash
CGO_ENABLED=1 ./build/go1.27.1/bin/go -C caddy test -race github.com/ducktype/caddy_proxy_cache
```

On Windows, check at least:

```powershell
.\bin\caddy-windows-amd64.exe version
.\bin\caddy-windows-amd64.exe list-modules
```

Successful compilation does not validate a destination server's PHP/TLS setup.
See `AGENTS.md` for repository editing conventions.
