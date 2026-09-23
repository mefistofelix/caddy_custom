# caddy_custom

Custom Caddy build with caddy-l4, forwardproxy, YAML support, and the local
proxy_cache and var_file modules.

Run `bash ./build.sh` on Linux x64 to build both binaries with Go 1.27.1:

- `bin/caddy-linux-amd64`
- `bin/caddy-windows-amd64.exe`

`caddy/go.mod.initial` is the source of truth for dependency versions. Each build
copies it to `caddy/go.mod` and runs `go mod tidy` before compiling. Only direct
dependencies are listed in `go.mod.initial`; generated `go.mod` and `go.sum`
files are not tracked in Git.
Run the `ci` workflow manually from GitHub Actions to build and publish both
binaries in a release named after the first 12 characters of the commit SHA.

The example configurations in `caddy/conf` target Linux with PHP-FPM Unix
sockets. Replace `example-user` and `REPLACE_WITH_BCRYPT_HASH` with your own
Basic Auth username and bcrypt hash before using authentication. The Windows
binary requires a configuration appropriate for Windows.
