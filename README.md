# caddy_custom

Custom Caddy build with caddy-l4, forwardproxy, YAML support, and the local
proxy_cache and var_file modules.

Run `bash ./build.sh` on Linux x64 to build both binaries with Go 1.24.3:

- `bin/caddy-linux-amd64`
- `bin/caddy-windows-amd64.exe`

The build uses the versions recorded in `caddy/go.mod` and `caddy/go.sum`.
Run the `ci` workflow manually from GitHub Actions to build and publish both
binaries in a release named after the first 12 characters of the commit SHA.

The example configurations in `caddy/conf` target Linux with PHP-FPM Unix
sockets. Replace `example-user` and `REPLACE_WITH_BCRYPT_HASH` with your own
Basic Auth username and bcrypt hash before using authentication. The Windows
binary requires a configuration appropriate for Windows.
