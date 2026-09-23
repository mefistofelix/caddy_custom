package var_file

import "net/http"
//import "io"
//import "io/fs"
//import "io/ioutil"
//import "slices"
//import "fmt"
//import "sync"
import "crypto/md5"
import "encoding/hex"
//import "path/filepath"
//import "time"
//import "os"
//import "bufio"
//import "strconv"
//import "strings"

//import "golang.org/x/sync/singleflight"

import "github.com/caddyserver/caddy/v2"
import "github.com/caddyserver/caddy/v2/modules/caddyhttp"
import "github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
import "github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"

import "go.uber.org/zap"

//--------------------------------------------------------------------------------
//static call like: UT{}.Method() or U.Method()
type ut struct {}
var U ut

func (ut) Md5(input string) string {
   hash := md5.Sum([]byte(input))
   ret := hex.EncodeToString(hash[:])
   return ret
}

//--------------------------------------------------------------------------------

type VarFile struct {
	logger *zap.Logger
	file_path string
}

//----------------------------------------------------------------------------------------

func NewVarFile() *VarFile {
	m := new(VarFile)
	return m
}

func init() {
	caddy.RegisterModule(VarFile{})
	httpcaddyfile.RegisterHandlerDirective("vae_file", parseCaddyfile)
}

func (VarFile) CaddyModule() caddy.ModuleInfo {
	mi := caddy.ModuleInfo{
		ID:  "http.handlers.var_file",
		New: func() caddy.Module {
			return NewVarFile()
		},
	}
	return mi
}

func (m *VarFile) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger()
	return nil
}

func (m *VarFile) Cleanup() error {
	return nil
}

//------------------------------------------------------------------------------

func (m *VarFile) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name
	m.file_path = d.Val()
	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m VarFile
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return m, err
}

//----------------------------------------------------------------------------------------


func (m VarFile) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	defer m.logger.Sync()
	var log = m.logger.Sugar()
	repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)

	log.Debugln("var_file ServeHTTP", m.file_path)

	//https://github.com/caddyserver/caddy/blob/0287009ee5fbe171e7a84f7d5b965992bb5488a7/modules/caddyhttp/intercept/intercept.go#L166
	repl.Set("var_file.xxx", "aaaa")

  return nil
}

//-----------------------------------------------------------------------------

var (
	_ caddyhttp.MiddlewareHandler = (*VarFile)(nil)
	_ caddy.Provisioner           = (*VarFile)(nil)
	_ caddy.CleanerUpper          = (*VarFile)(nil)
	_ caddyfile.Unmarshaler       = (*VarFile)(nil)

	//_ http.ResponseWriter = (*FileResponseWriter)(nil)
)