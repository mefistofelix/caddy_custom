# Agent instructions

These instructions apply to the entire repository. Read `README.md` and the
relevant source files before making changes. Explicit user instructions take
precedence over the conventions below. Keep project documentation in English.

## Purpose and architecture

This project builds a custom Caddy executable containing the standard modules,
caddy-l4, forwardproxy, caddy-yaml, and two local HTTP middleware modules.

- `caddy/main.go` registers modules through side-effect imports and delegates
  execution to the Caddy CLI.
- `caddy_proxy_cache/proxy_cache.go` implements the disk response cache.
- `caddy_var_file/var_file.go` loads native request variables from JSON/YAML files.
- Relative `replace` directives connect both local modules to the main build.
- There is no frontend, application framework, or Node.js dependency.

## Build decisions to preserve

1. **`caddy/go.mod.initial` is the source of truth.** Keep it minimal: direct
   dependencies and local replacements only.
2. Every build copies that file to `caddy/go.mod` and runs `go mod tidy`.
   Preserve this regeneration step.
3. `caddy/go.mod` and `caddy/go.sum` are generated and ignored by Git. Do not
   force-add them or make permanent version changes only in those files.
4. Do not manually list indirect dependencies in the initial manifest or add
   blanket `go get -u` operations to the build. Resolve indirect dependencies
   from the selected direct modules' manifests.
5. The main build runs on Linux x64 and produces only Linux/amd64 and
   Windows/amd64 with `CGO_ENABLED=0`. Preserve the asset names
   `caddy-linux-amd64` and `caddy-windows-amd64.exe`.
6. `build.sh` downloads its own Go compiler. `GOTOOLCHAIN=local` prevents implicit
   toolchain switching. Do not reintroduce `xcaddy` or `build.sh.old` unless
   explicitly changing the build design at the user's request.
7. Keep Bash scripts and workflows LF-terminated, as specified in `.gitattributes`.
   Toolchains, binaries, and runtime data stay outside Git.

## Updating Go, Caddy, or plugins

- For Caddy and external plugins, use the latest commits on their upstream
  default branches (`master`/`main`), including unreleased changes. Do not use
  stable-only selection or assume `@latest` means the branch head.
- Resolve the selected commit to its Go module version and record that version
  in the initial manifest. A tagged version is acceptable when it identifies
  the actual branch head. Do not introduce automatic moving-branch updates on
  every build unless requested. Keep the Go compiler version explicit.
- Update `go_version` in `build.sh`, the `go` directive in
  `caddy/go.mod.initial`, and both local modules' `go.mod` files.
- Update direct dependencies in `caddy/go.mod.initial` and the README version table.
- For a new external module, also add its import to `caddy/main.go`.
- Rebuild from the initial manifest. A working generated `go.mod` alone does not
  prove that the actual build works.
- Investigate and report incompatibilities. Do not hide them by editing the
  global Go module cache or introducing blanket indirect dependency upgrades.
- Do not claim full dependency locking: resolved manifests are intentionally
  not versioned in this project.

## Workflow and releases

Keep `.github/workflows/ci.yml` minimal and aligned with the existing template:

- Manual `workflow_dispatch`; no automatic push trigger.
- One `ubuntu-latest` job fetches the requested SHA and runs `bash ./build.sh`.
- Use only `contents: write` with the automatic GitHub token for publication.
- Tag releases with the first 12 characters of the commit SHA.
- Publish both executables using `gh release create ... || gh release upload ... --clobber`.
- Do not add a separate `gh release view` lookup to the workflow.

Publishing a release is not a deployment. When releasing, verify the workflow
outcome and both assets. Documentation-only changes do not require rebuilding
unchanged executables.

## Configuration

- `caddy/conf/Caddyfile` is the only checked-in example configuration. Do not
  restore JSON or YAML copies without an explicit request. YAML adapter support
  in the executable is independent of keeping YAML examples.
- The example targets Linux, multiple sites, and PHP-FPM. Its Unix sockets and
  local TLS authorization endpoint are external services, not bundled features.
- Windows needs paths and upstreams appropriate for Windows.
- Keep authentication placeholders in shared examples. Never commit real
  credentials, password hashes, operational logs, or cache files.
- `adapt` checks conversion; `validate` also provisions the configuration. The
  placeholder hash is invalid, so adaptation alone must not be reported as
  successful validation of the unmodified example.

## Middleware behavior

### proxy_cache

This middleware caches GET/HEAD responses on disk, requires the HTTP `root`
variable, and coalesces cache updates with `singleflight` per complete cache key.
Preserve that granularity and immediate stale serving during background refresh.
By default, all request headers and the body contribute to the key before
coalescing. An explicit placeholder key replaces these request dimensions;
document the caller's responsibility for cookie, authorization, and body dimensions
not covered by upstream `Vary` or bypass. Automatic `Vary` uses the primary response
as the on-disk entry point and derived keys for secondary variants. Recheck each
waiter's variant after a fill; never share a mismatched response, including stale
entries. Preserve incoming header snapshots, per-variant singleflight, and complete
publication when `Vary` changes or disappears. `ignore_headers Vary` disables this
selection. Keep metadata in the response file; do not add a separate index service.
Keep the document-root namespace and policy fingerprint. Coalesce by the full
cache path, so identical template values in separate roots do not share a fill.
Resolve `storage_path` request placeholders before lookup without mutating shared
configuration. Track resolved directories safely for cleanup; dynamic paths are
rediscovered on their first eligible request after reload or restart.

Keep the production implementation minimal and in its existing single file.
The supported options are `methods`, `storage_path`, `key`, `bypass` (Caddy CEL),
`inactive`, `max_age`, `wait_timeout`, `valid`, and `ignore_headers`. Reuse Caddy's
replacer and CEL matcher rather than building a separate expression engine.
An explicit CEL expression replaces the default Authorization/nocache bypass;
Range and Upgrade bypass remain unconditional. Buffer eligible request bodies
once and use independent readers for fills and fallbacks.

`ignore_headers` overrides response policy, never removes headers. Policy is evaluated
at final headers; only a complete successful response is published. Uncacheable
responses must not be shared among waiting requests. Create response files only
after final headers approve caching. Stream all miss responses to their owner
while capturing eligible bodies; header-rejected responses never touch disk.
Only the request goroutine may write to its client. Flush outgoing stream chunks
through `http.ResponseController` so Caddy's wrappers are respected,
close pipes on timeout/cancellation, and keep waiters on complete published files.
Owner stream failures must not be inherited by waiting clients. Preserve response
headers and isolate background request state from the original request. Keep the
bounded-memory backpressure tradeoff documented; do not imply Nginx's buffering
architecture or broadcast incomplete bodies to waiters.

Keep freshness separate from retention. Mtime tracks last access only when
`inactive` is enabled. Immutable creation time and stored durations enforce
`max_age` and keep `Age` independent of touches. Enforce both retention limits
on lookup and cleanup, including for stale entries. Stop the cleaner with the
module context; do not leave timers running after cleanup. Temporary files stay
on the cache filesystem and abandoned fills must be removed.

The README describes the implemented policy and remaining limitations; do not
imply complete RFC or Nginx compatibility. Nginx is a reference for relevant
configuration semantics, not a mandate to reproduce its cache architecture.
Debug logs can include Authorization and full request URLs.

When changing this middleware, check cache miss/hit, expiry, concurrent requests,
bypass, upstream failure, and filesystem errors. Do not turn a dependency update
into an unrelated middleware rewrite.

### var_file

The JSON module is `http.handlers.var_file`; the directive is `var_file path root`.
Keep production code in one file, within the user's 100-line limit, using only
the standard library and libraries/APIs already included by Caddy. Do not restore
the old `vae_file` typo or add alternative placeholder syntaxes.

Resolve file path placeholders per request. Load once per request by default;
missing files skip unless `required` is set. Other errors must not silently skip.
Expose one flat map of leaves via `caddyhttp.SetVar`, including numeric array
indices, so variables survive proxy-cache request cloning. Use full placeholders
such as `{http.vars.app.database.host}`: Caddy's vars shorthand cannot expand
dotted keys. Reject ambiguous object keys, and never mutate cached leaf maps.

Optional `cache`, `stale`, and `max_entries` control fresh lifetime, additional
stale lifetime and exact LRU capacity. Keep cache state per directive instance,
coalesce fills per resolved path, and never hold the metadata mutex across I/O or
decoding. Failed refreshes must not renew deadlines; stale is never indefinite.
Do not add watchers or cleaner timers. Document that LRU eviction scans the
bounded entries and that capacity counts files rather than bytes.

Test file formats, nesting, errors, default reloads, snapshots, LRU, stale
refresh, expiry, independent paths and cancellation. Run var_file tests with
the race detector for concurrency changes and verify Caddyfile adaptation.

## Verification and delivery

For build or dependency changes:

```bash
bash -n ./build.sh
bash ./build.sh
./build/go1.27.1/bin/go -C caddy test . github.com/ducktype/caddy_proxy_cache github.com/ducktype/caddy_var_file
git diff --check
```

Adjust the compiler path to match `build.sh`. Check `version` and `list-modules`
on Windows when available. Cache behavior tests live in
`caddy_proxy_cache/proxy_cache_test.go`; run them with `-race` when changing
concurrency (requires `CGO_ENABLED=1` and a C compiler). Distinguish compilation,
CLI execution, in-process handler tests, and checks against a running server.

For documentation changes, verify file paths, commands, versions, and agreement
with the source. Avoid repeating unchanged binary builds solely for documentation.

Review the diff and staging area before committing, including the absence of
generated build inputs. Summarize changes, checks actually performed, and any
commit or release. Do not claim unperformed checks passed.
