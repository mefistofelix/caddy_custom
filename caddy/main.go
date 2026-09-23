package main

import (
	caddycmd "github.com/caddyserver/caddy/v2/cmd"

	// plug in Caddy modules here
	_ "github.com/caddyserver/caddy/v2/modules/standard" 
	_ "github.com/mholt/caddy-l4"
	_ "github.com/caddyserver/forwardproxy"
	_ "github.com/abiosoft/caddy-yaml"
	_ "github.com/ducktype/caddy_proxy_cache"
	_ "github.com/ducktype/caddy_var_file"
)

func main() {
	caddycmd.Main()
}
