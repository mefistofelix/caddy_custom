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
- `caddy_var_file/var_file.go` is a prototype, not a completed feature.
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
variable, and coalesces cache updates with `singleflight`. Freshness is 300
seconds; stale responses may be served during background refresh. Authorization,
Range, and the implemented `nocache=1` signals bypass caching.

Settings are currently constants in the source. Do not document unimplemented
Caddyfile options. This is not a complete HTTP cache: session cookies, Vary,
response status codes, and cache-control headers need explicit consideration.
Headers omitted from replay can still be stored on disk. Debug logs can include
Authorization and full request URLs.

When changing this middleware, check cache miss/hit, expiry, concurrent requests,
bypass, upstream failure, and filesystem errors. Do not turn a dependency update
into an unrelated middleware rewrite.

### var_file

The JSON module is `http.handlers.var_file`; the registered Caddyfile directive
is `vae_file`. It does not read files, sets a fixed placeholder, and does not
call `next`. Document these as current limitations. Completing or correcting
them is a behavior change, not part of routine dependency maintenance.

## Verification and delivery

For build or dependency changes:

```bash
bash -n ./build.sh
bash ./build.sh
./build/go1.27.1/bin/go -C caddy test . github.com/ducktype/caddy_proxy_cache github.com/ducktype/caddy_var_file
git diff --check
```

Adjust the compiler path to match `build.sh`. Check `version` and `list-modules`
on Windows when available. No functional test files currently exist: report
`[no test files]` accurately, and distinguish compilation, CLI execution, and
real HTTP behavior checks.

For documentation changes, verify file paths, commands, versions, and agreement
with the source. Avoid repeating unchanged binary builds solely for documentation.

Review the diff and staging area before committing, including the absence of
generated build inputs. Summarize changes, checks actually performed, and any
commit or release. Do not claim unperformed checks passed.
