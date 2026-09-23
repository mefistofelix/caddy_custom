package var_file

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "strconv"
 "strings"
 "sync"
 "time"
 "github.com/caddyserver/caddy/v2"
 "github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
 "github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
 "github.com/caddyserver/caddy/v2/modules/caddyhttp"
 "go.yaml.in/yaml/v3"
 "golang.org/x/sync/singleflight"
)
type VarFile struct {
 Path string `json:"path"`
 Root string `json:"root"`
 Required bool `json:"required,omitempty"`
 Cache caddy.Duration `json:"cache,omitempty"`
 Stale caddy.Duration `json:"stale,omitempty"`
 MaxEntries int `json:"max_entries,omitempty"`
 ctx context.Context
 mu sync.Mutex
 entries map[string]*entry
 flights singleflight.Group
 clock uint64
}
type entry struct { values map[string]any; loaded time.Time; used uint64 }
func init() {
 caddy.RegisterModule(new(VarFile))
 httpcaddyfile.RegisterDirective("var_file", func(h httpcaddyfile.Helper) ([]httpcaddyfile.ConfigValue, error) { m := new(VarFile); if err := m.UnmarshalCaddyfile(h.Dispenser); err != nil { return nil, err }; return h.NewRoute(nil, m), nil })
 httpcaddyfile.RegisterDirectiveOrder("var_file", httpcaddyfile.After, "root")
}
func (*VarFile) CaddyModule() caddy.ModuleInfo { return caddy.ModuleInfo{ID: "http.handlers.var_file", New: func() caddy.Module { return new(VarFile) }} }
func (m *VarFile) Provision(ctx caddy.Context) error {
 if m.Path == "" || m.Root == "" || strings.ContainsAny(m.Root, "{}[]/ \t\r\n") || m.Cache < 0 || m.Stale < 0 || m.MaxEntries < 0 { return fmt.Errorf("invalid var_file path, root or cache settings") }
 if m.Cache == 0 && (m.Stale != 0 || m.MaxEntries != 0) { return fmt.Errorf("stale and max_entries require cache > 0") }
 if m.MaxEntries == 0 { m.MaxEntries = 128 }; m.ctx, m.entries = ctx, make(map[string]*entry); return nil
}
func (m *VarFile) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
 d.Next(); if !d.AllArgs(&m.Path, &m.Root) { return d.ArgErr() }
 for d.NextBlock(0) {
  option := d.Val(); args := d.RemainingArgs()
  if option == "required" && len(args) == 0 { m.Required = true; continue }; if len(args) != 1 { return d.ArgErr() }
  switch option {
  case "cache", "stale": v, err := caddy.ParseDuration(args[0]); if err != nil || v < 0 { return d.Err("invalid duration") }; if option == "cache" { m.Cache = caddy.Duration(v) } else { m.Stale = caddy.Duration(v) }
  case "max_entries": v, err := strconv.Atoi(args[0]); if err != nil || v <= 0 { return d.Err("max_entries must be positive") }; m.MaxEntries = v
  default: return d.Errf("unknown var_file option: %s", option)
  }
 }; return nil
}
func (m *VarFile) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
 path, err := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer).ReplaceOrErr(m.Path, true, true); if err != nil { return err }
 path, err = filepath.Abs(path); if err != nil { return err }; values, err := m.load(r.Context(), path)
 if err != nil && !(os.IsNotExist(err) && !m.Required) { return fmt.Errorf("var_file %s: %w", path, err) }
 for key, value := range values { caddyhttp.SetVar(r.Context(), key, value) }; return next.ServeHTTP(w, r)
}
func (m *VarFile) load(ctx context.Context, path string) (map[string]any, error) {
 if m.Cache == 0 { return m.read(path) }
 m.mu.Lock(); old := m.entries[path]; m.clock++; if old != nil { old.used = m.clock }; m.mu.Unlock()
 if old != nil && time.Since(old.loaded) < time.Duration(m.Cache) { return old.values, nil }
 result := m.flights.DoChan(path, func() (any, error) {
  m.mu.Lock(); fresh := m.entries[path]; m.mu.Unlock()
  if fresh != nil && time.Since(fresh.loaded) < time.Duration(m.Cache) { return fresh.values, nil }
  if err := m.ctx.Err(); err != nil { return nil, err }; values, err := m.read(path); if err != nil { return nil, err }
  m.mu.Lock(); defer m.mu.Unlock(); if err := m.ctx.Err(); err != nil { return nil, err }
  if m.entries[path] == nil && len(m.entries) >= m.MaxEntries {
   victim := ""; var oldest uint64 = ^uint64(0); for key, e := range m.entries { if e.used < oldest { victim, oldest = key, e.used } }; delete(m.entries, victim)
  }
  m.clock++; m.entries[path] = &entry{values: values, loaded: time.Now(), used: m.clock}; return values, nil
 })
 if old != nil && time.Since(old.loaded)-time.Duration(m.Cache) < time.Duration(m.Stale) { return old.values, nil }
 select { case res := <-result: if res.Err != nil { return nil, res.Err }; return res.Val.(map[string]any), nil; case <-ctx.Done(): return nil, ctx.Err(); case <-m.ctx.Done(): return nil, m.ctx.Err() }
}
func (m *VarFile) read(path string) (map[string]any, error) {
 body, err := os.ReadFile(path); if err != nil { return nil, err }; var data, extra any
 if ext := strings.ToLower(filepath.Ext(path)); ext == ".yaml" || ext == ".yml" {
  d := yaml.NewDecoder(bytes.NewReader(body)); if err = d.Decode(&data); err != nil { return nil, err }; if d.Decode(&extra) != io.EOF { return nil, fmt.Errorf("expected one YAML document") }
  body, err = json.Marshal(data); if err != nil { return nil, err }
 }
 d := json.NewDecoder(bytes.NewReader(body)); d.UseNumber(); if err = d.Decode(&data); err != nil { return nil, err }; if d.Decode(&extra) != io.EOF { return nil, fmt.Errorf("expected one JSON value") }
 values := make(map[string]any); err = flatten(values, m.Root, data); return values, err
}
func flatten(values map[string]any, key string, data any) error {
 switch v := data.(type) {
 case map[string]any:
  for name, value := range v { if name == "" || strings.ContainsAny(name, ".{}") { return fmt.Errorf("object keys must be nonempty and contain no dots or braces") }; if err := flatten(values, key+"."+name, value); err != nil { return err } }
 case []any: for i, value := range v { if err := flatten(values, key+"."+strconv.Itoa(i), value); err != nil { return err } }
 default: values[key] = data
 }; return nil
}
