package var_file

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func module(t *testing.T, cached bool) *VarFile {
	t.Helper()
	m := &VarFile{Path: "{http.vars.path}", Root: "app"}
	if cached {
		m.Cache, m.Stale, m.MaxEntries = caddy.Duration(time.Hour), caddy.Duration(time.Hour), 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := m.Provision(caddy.Context{Context: ctx}); err != nil {
		t.Fatal(err)
	}
	return m
}
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func request(m *VarFile, path, template string) (string, error) {
	w := httptest.NewRecorder()
	r := caddyhttp.PrepareRequest(httptest.NewRequest("GET", "http://example.test/", nil), caddy.NewReplacer(), w, nil)
	caddyhttp.SetVar(r.Context(), "path", path)
	var result string
	err := m.ServeHTTP(w, r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
		result = r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer).ReplaceAll(template, "")
		return nil
	}))
	return result, err
}
func age(m *VarFile, path string, elapsed time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[path].loaded = time.Now().Add(-elapsed)
}
func finishRefresh(m *VarFile, path string) {
	m.flights.Do(path, func() (any, error) { return nil, nil })
}

func TestFileVariablesAndDefaultReload(t *testing.T) {
	for _, tc := range []struct{ ext, body string }{
		{".json", `{"db":{"host":"localhost"},"servers":[{"port":8080}],"enabled":true,"big":9007199254740993,"none":null}`},
		{".yaml", "db:\n  host: localhost\nservers:\n  - port: 8080\nenabled: true\nbig: 9007199254740993\nnone: null\n"},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			m := module(t, false)
			path := filepath.Join(t.TempDir(), "settings"+tc.ext)
			write(t, path, tc.body)
			got, err := request(m, path, "{http.vars.app.db.host}|{http.vars.app.servers.0.port}|{http.vars.app.enabled}|{http.vars.app.big}|{http.vars.app.none}")
			if err != nil || got != "localhost|8080|true|9007199254740993|" {
				t.Fatalf("%q %v", got, err)
			}
			write(t, path, `{"db":{"host":"changed"}}`)
			got, err = request(m, path, "{http.vars.app.db.host}")
			if err != nil || got != "changed" {
				t.Fatalf("default did not reload: %q %v", got, err)
			}
		})
	}
}

func TestMissingInvalidAndSnapshot(t *testing.T) {
	m := module(t, false)
	path := filepath.Join(t.TempDir(), "settings.json")
	if got, err := request(m, path, "next:{http.vars.app.name}"); got != "next:" || err != nil {
		t.Fatalf("missing: %q %v", got, err)
	}
	m.Required = true
	if _, err := request(m, path, "next"); !os.IsNotExist(rootError(err)) {
		t.Fatalf("required missing: %v", err)
	}
	m.Required = false
	for _, body := range []string{`broken`, `{} {}`, `{"a.b":1}`, `{"":1}`} {
		write(t, path, body)
		if _, err := request(m, path, "next"); err == nil {
			t.Fatalf("accepted invalid data: %s", body)
		}
	}
	write(t, path, `{"name":"before"}`)
	w := httptest.NewRecorder()
	r := caddyhttp.PrepareRequest(httptest.NewRequest("GET", "/", nil), caddy.NewReplacer(), w, nil)
	caddyhttp.SetVar(r.Context(), "path", path)
	err := m.ServeHTTP(w, r, caddyhttp.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) error {
		write(t, path, `{"name":"after"}`)
		if got := caddyhttp.GetVar(r.Context(), "app.name"); got != "before" {
			return fmt.Errorf("snapshot changed: %v", got)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := request(m, "", "next"); err == nil {
		t.Fatal("empty path placeholder accepted")
	}
	for _, body := range []string{"a: 1\n---\na: 2", "a: ["} {
		yamlPath := filepath.Join(filepath.Dir(path), "invalid.yaml")
		write(t, yamlPath, body)
		if _, err := request(m, yamlPath, "next"); err == nil {
			t.Fatalf("invalid YAML accepted: %s", body)
		}
	}
}
func rootError(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

func TestCacheLRUAndConcurrentFiles(t *testing.T) {
	m := module(t, true)
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json"), filepath.Join(dir, "c.json")}
	for i, path := range paths {
		write(t, path, fmt.Sprintf(`{"id":%d}`, i))
	}
	var wg sync.WaitGroup
	results := make(chan map[string]any, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := m.load(context.Background(), paths[0])
			if err != nil {
				t.Error(err)
			}
			results <- v
		}()
	}
	wg.Wait()
	close(results)
	var first map[string]any
	for v := range results {
		if first == nil {
			first = v
		}
		if reflect.ValueOf(v).Pointer() != reflect.ValueOf(first).Pointer() {
			t.Error("fill was not shared")
		}
	}
	for _, i := range []int{1, 0, 2} {
		if _, err := m.load(context.Background(), paths[i]); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	_, evicted := m.entries[paths[1]]
	count := len(m.entries)
	m.mu.Unlock()
	if evicted || count != 2 {
		t.Fatalf("LRU eviction: b retained=%t entries=%d", evicted, count)
	}
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	if got, err := request(m, paths[0], "{http.vars.app.id}"); got != "0" || err != nil {
		t.Fatalf("fresh cache touched disk: %q %v", got, err)
	}
}

func TestStaleRefreshAndFailureDeadline(t *testing.T) {
	m := module(t, true)
	path := filepath.Join(t.TempDir(), "settings.json")
	write(t, path, `{"name":"old"}`)
	if _, err := m.load(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	age(m, path, 90*time.Minute)
	write(t, path, `{"name":"new"}`)
	if got, err := request(m, path, "{http.vars.app.name}"); got != "old" || err != nil {
		t.Fatalf("stale: %q %v", got, err)
	}
	finishRefresh(m, path)
	if got, err := request(m, path, "{http.vars.app.name}"); got != "new" || err != nil {
		t.Fatalf("refresh: %q %v", got, err)
	}
	age(m, path, 90*time.Minute)
	write(t, path, "broken")
	if got, err := request(m, path, "{http.vars.app.name}"); got != "new" || err != nil {
		t.Fatalf("stale on error: %q %v", got, err)
	}
	finishRefresh(m, path)
	age(m, path, 3*time.Hour)
	if _, err := request(m, path, "next"); err == nil {
		t.Fatal("served stale beyond deadline")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := request(m, path, "next:{http.vars.app.name}"); got != "next:" || err != nil {
		t.Fatalf("expired missing skip: %q %v", got, err)
	}
}

func TestFlightCancellationAndIndependentPaths(t *testing.T) {
	m := module(t, true)
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked.json")
	other := filepath.Join(dir, "other.json")
	write(t, other, `{"id":2}`)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	flight := m.flights.DoChan(path, func() (any, error) { <-release; return map[string]any{"app.id": json.Number("1")}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.load(ctx, path); err != context.Canceled {
		t.Fatalf("cancelled waiter: %v", err)
	}
	if got, err := request(m, other, "{http.vars.app.id}"); got != "2" || err != nil {
		t.Fatalf("independent path: %q %v", got, err)
	}
	unblock()
	if result := <-flight; result.Err != nil {
		t.Fatal(result.Err)
	}
}

func TestCaddyfileAdaptationAndValidation(t *testing.T) {
	config := `http://localhost:8080 {
 var_file "/srv/{vars.site}/settings.json" app {
  cache 30s
  stale 5m
  max_entries 128
  required
 }
 respond "{http.vars.app.name}"
}`
	body, _, err := caddyconfig.GetAdapter("caddyfile").Adapt([]byte(config), nil)
	if err != nil || !strings.Contains(string(body), `"handler":"var_file"`) || !strings.Contains(string(body), `http.vars.app.name`) {
		t.Fatalf("adapt: %s %v", body, err)
	}
	var m VarFile
	if err := m.UnmarshalCaddyfile(caddyfile.NewTestDispenser("var_file data.json app {\n cache 30s\n stale 5m\n max_entries 2\n required\n}")); err != nil {
		t.Fatal(err)
	}
	if !m.Required || m.MaxEntries != 2 || m.Cache != caddy.Duration(30*time.Second) {
		t.Fatalf("parsed: %+v", &m)
	}
	for _, input := range []string{"var_file", "var_file a b c", "var_file a b {\n required yes\n}", "var_file a b {\n cache -1s\n}", "var_file a b {\n max_entries 0\n}", "var_file a b {\n unknown 1\n}"} {
		var m VarFile
		if err := m.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)); err == nil {
			t.Fatalf("invalid config accepted: %s", input)
		}
	}
	for _, m := range []*VarFile{{Path: "x", Root: "app", Stale: 1}, {Path: "x", Root: "app", MaxEntries: 1}, {Path: "x", Root: ""}, {Path: "x", Root: "app", Cache: -1}} {
		if err := m.Provision(caddy.Context{Context: context.Background()}); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
}
