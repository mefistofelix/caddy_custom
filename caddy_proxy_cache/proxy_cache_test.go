package proxy_cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

func newTestCache(t *testing.T) *ProxyCache {
	t.Helper()
	m := NewProxyCache()
	m.cache_dir, m.logger = t.TempDir(), zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	t.Cleanup(cancel)
	return m
}

func request(h http.Header) *http.Request {
	r := httptest.NewRequest("GET", "http://example.test/page?a=1", nil)
	r.Header = h.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	r = caddyhttp.PrepareRequest(r, caddy.NewReplacer(), httptest.NewRecorder(), nil)
	caddyhttp.SetVar(r.Context(), "root", "/test/public")
	caddyhttp.SetVar(r.Context(), "root_name", "example")
	return r
}

func perform(m *ProxyCache, r *http.Request, next caddyhttp.Handler) (*httptest.ResponseRecorder, error) {
	w := httptest.NewRecorder()
	err := m.ServeHTTP(w, r, next)
	return w, err
}

func files(t *testing.T, m *ProxyCache) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(m.cache_dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestResponsePolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		headers http.Header
		cached  bool
	}{
		{"default", 200, nil, true},
		{"redirect", 301, nil, true},
		{"not-found", 404, nil, true},
		{"error", 500, nil, false},
		{"private", 200, http.Header{"Cache-Control": {"public, private=\"Set-Cookie\""}}, false},
		{"no-store", 200, http.Header{"Cache-Control": {"no-store"}}, false},
		{"no-cache", 200, http.Header{"Cache-Control": {"no-cache"}}, false},
		{"max-age-zero", 200, http.Header{"Cache-Control": {"max-age=0"}}, false},
		{"invalid-age", 200, http.Header{"Cache-Control": {"max-age=bad"}}, false},
		{"duplicate-age", 200, http.Header{"Cache-Control": {"max-age=0, max-age=60"}}, false},
		{"expired-age", 200, http.Header{"Cache-Control": {"max-age=60"}, "Age": {"90"}}, false},
		{"expired-date", 200, http.Header{"Cache-Control": {"max-age=60"}, "Date": {time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)}}, false},
		{"expires", 200, http.Header{"Expires": {time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)}}, true},
		{"past-expires", 200, http.Header{"Expires": {time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)}}, false},
		{"invalid-expires", 200, http.Header{"Expires": {"0"}}, false},
		{"shared-precedence", 200, http.Header{"Cache-Control": {"max-age=0, s-maxage=60"}, "Expires": {"0"}}, true},
		{"set-cookie", 200, http.Header{"Set-Cookie": {"session=private; HttpOnly"}}, false},
		{"vary-star", 200, http.Header{"Vary": {"Accept-Encoding, *"}}, false},
		{"vary", 200, http.Header{"Vary": {"Accept-Language"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestCache(t)
			var calls atomic.Int32
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				calls.Add(1)
				for k, v := range tc.headers {
					w.Header()[k] = v
				}
				w.WriteHeader(tc.status)
				_, err := io.WriteString(w, "response")
				return err
			})
			for i := 0; i < 2; i++ {
				w, err := perform(m, request(nil), next)
				if err != nil || w.Code != tc.status || w.Body.String() != "response" {
					t.Fatalf("code=%d body=%q err=%v", w.Code, w.Body.String(), err)
				}
				for k, v := range tc.headers {
					if w.Header().Get(k) != v[0] {
						t.Fatalf("lost response header %s", k)
					}
				}
			}
			want := int32(2)
			if tc.cached {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("upstream calls=%d want=%d", calls.Load(), want)
			}
			if !tc.cached && len(files(t, m)) != 0 {
				t.Fatal("uncacheable response was retained")
			}
		})
	}
}

func TestConfiguration(t *testing.T) {
	m := newTestCache(t)
	config := "proxy_cache {\n valid 200 302 10m\n valid 404 20s\n ignore_headers Cache-Control Expires Set-Cookie\n}"
	if err := m.UnmarshalCaddyfile(caddyfile.NewTestDispenser(config)); err != nil {
		t.Fatal(err)
	}
	if m.Valid["200"] != caddy.Duration(10*time.Minute) || m.Valid["404"] != caddy.Duration(20*time.Second) {
		t.Fatal(m.Valid)
	}
	var calls int
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls++
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Expires", "0")
		w.Header().Set("Set-Cookie", "configured=override")
		_, err := io.WriteString(w, "ok")
		return err
	})
	for i := 0; i < 2; i++ {
		w, err := perform(m, request(nil), next)
		if err != nil || w.Header().Get("Set-Cookie") == "" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("ignore_headers changed replay: %v %v", w.Header(), err)
		}
	}
	if calls != 1 {
		t.Fatal("ignore_headers did not override eligibility")
	}
	for _, text := range []string{"proxy_cache unexpected", "proxy_cache {\n valid nope 1m\n}", "proxy_cache {\n valid 200 -1s\n}", "proxy_cache {\n unknown value\n}"} {
		if err := new(ProxyCache).UnmarshalCaddyfile(caddyfile.NewTestDispenser(text)); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestPerVariantSingleflight(t *testing.T) {
	m := newTestCache(t)
	started := make(chan string, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls.Add(1)
		started <- r.Header.Get("Cookie")
		<-release
		w.Header().Set("Vary", "Accept-Language")
		_, err := io.WriteString(w, r.Header.Get("Cookie")+"/"+r.Header.Get("Accept-Language"))
		return err
	})
	var wg sync.WaitGroup
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cookie, lang := "user=a", "en"
			if i%2 != 0 {
				cookie = "user=b"
			}
			if i%4 >= 2 {
				lang = "it"
			}
			w, err := perform(m, request(http.Header{"Cookie": {cookie}, "Accept-Language": {lang}}), next)
			if err != nil {
				errs <- err
			} else if w.Body.String() != cookie+"/"+lang {
				errs <- fmt.Errorf("mixed variant: %q", w.Body.String())
			}
		}(i)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("different variants serialized behind the same lock")
		}
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("upstream calls=%d want=4", calls.Load())
	}
}

func TestConfiguredFreshness(t *testing.T) {
	for _, tc := range []struct {
		config  string
		status  int
		control string
		ttl     int64
	}{
		{"valid 1m", 200, "", 60},
		{"valid 1m", 404, "", 0},
		{"valid any 2m\n valid 404 20s", 404, "", 20},
		{"valid any 2m", 503, "", 120},
		{"valid any 2m\n valid 500 0s", 500, "max-age=60", 0},
		{"valid 200 1m", 200, "max-age=30, s-maxage=90", 90},
	} {
		t.Run(fmt.Sprintf("%s/%d/%s", tc.config, tc.status, tc.control), func(t *testing.T) {
			m := newTestCache(t)
			if err := m.UnmarshalCaddyfile(caddyfile.NewTestDispenser("proxy_cache {\n" + tc.config + "\n}")); err != nil {
				t.Fatal(err)
			}
			before := time.Now().Unix()
			w, err := perform(m, request(nil), caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("Cache-Control", tc.control)
				w.WriteHeader(tc.status)
				return nil
			}))
			if err != nil || w.Code != tc.status {
				t.Fatalf("status=%d err=%v", w.Code, err)
			}
			paths := files(t, m)
			if tc.ttl == 0 {
				if len(paths) != 0 {
					t.Fatal("disabled status cached")
				}
				return
			}
			if len(paths) != 1 {
				t.Fatalf("cache files=%d", len(paths))
			}
			data, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			expiry, _, _ := strings.Cut(string(data), " ")
			seconds, err := strconv.ParseInt(expiry, 10, 64)
			if err != nil || seconds < before+tc.ttl || seconds > time.Now().Unix()+tc.ttl {
				t.Fatalf("expiry=%q TTL=%d", expiry, tc.ttl)
			}
		})
	}
}

func TestUncacheableResponsesAreNotShared(t *testing.T) {
	m := newTestCache(t)
	var calls atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		id := calls.Add(1)
		w.Header().Set("Set-Cookie", fmt.Sprintf("session=%d", id))
		time.Sleep(15 * time.Millisecond)
		_, err := fmt.Fprintf(w, "%d", id)
		return err
	})
	var wg sync.WaitGroup
	results := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := perform(m, request(nil), next)
			if err != nil {
				results <- "error:" + err.Error()
				return
			}
			if w.Header().Get("Set-Cookie") != "session="+w.Body.String() {
				results <- "wrong-cookie"
				return
			}
			results <- w.Body.String()
		}()
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for body := range results {
		if seen[body] || strings.HasPrefix(body, "error:") || body == "wrong-cookie" {
			t.Errorf("shared private response: %s", body)
		}
		seen[body] = true
	}
	if calls.Load() != 20 || len(seen) != 20 {
		t.Fatalf("calls=%d unique=%d", calls.Load(), len(seen))
	}
	if len(files(t, m)) != 0 {
		t.Fatal("temporary private responses leaked")
	}
}

func TestPolicyChangesDoNotReuseOverrides(t *testing.T) {
	m := newTestCache(t)
	m.IgnoreHeaders = []string{"Set-Cookie"}
	var calls int
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls++
		w.Header().Set("Set-Cookie", fmt.Sprintf("session=%d", calls))
		return nil
	})
	if _, err := perform(m, request(nil), next); err != nil {
		t.Fatal(err)
	}
	m.IgnoreHeaders = nil
	w, err := perform(m, request(nil), next)
	if err != nil || calls != 2 || w.Header().Get("Set-Cookie") != "session=2" {
		t.Fatalf("reused response from overridden policy: calls=%d headers=%v err=%v", calls, w.Header(), err)
	}
}

func TestStaleRefreshKeepsConcurrentRequestsFast(t *testing.T) {
	m := newTestCache(t)
	first := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { _, err := io.WriteString(w, "old"); return err })
	if _, err := perform(m, request(nil), first); err != nil {
		t.Fatal(err)
	}
	path := files(t, m)[0]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, suffix, _ := strings.Cut(string(data), " ")
	if err := os.WriteFile(path, []byte(strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10)+" "+suffix), 0600); err != nil {
		t.Fatal(err)
	}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		defer close(done)
		if r.Context().Err() != nil {
			return r.Context().Err()
		}
		if caddyhttp.GetVar(r.Context(), "root_name") != "example" {
			return errors.New("lost routing variables")
		}
		caddyhttp.SetVar(r.Context(), "root_name", "background-only")
		_, err := io.WriteString(w, "new")
		return err
	})
	r := request(nil)
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(ctx)
	w, err := perform(m, r, next)
	if err != nil || w.Body.String() != "old" {
		t.Fatalf("stale response: %q %v", w.Body.String(), err)
	}
	cancel()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh not started")
	}
	for i := 0; i < 15; i++ {
		w, err := perform(m, request(nil), next)
		if err != nil || w.Body.String() != "old" {
			t.Fatalf("concurrent stale: %q %v", w.Body.String(), err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("parallel refreshes=%d", calls.Load())
	}
	close(release)
	<-done
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(path)
		if strings.HasSuffix(string(b), "new") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	w, err = perform(m, request(nil), first)
	if err != nil || w.Body.String() != "new" {
		t.Fatalf("refreshed response: %q %v", w.Body.String(), err)
	}
	if caddyhttp.GetVar(r.Context(), "root_name") != "example" {
		t.Fatal("refresh mutated original request variables")
	}
}

func TestFailedFillIsNotPublished(t *testing.T) {
	for _, kind := range []string{"upstream-error", "truncated-body"} {
		t.Run(kind, func(t *testing.T) {
			m := newTestCache(t)
			next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				w.Header().Set("Content-Length", "20")
				io.WriteString(w, "partial")
				if kind == "upstream-error" {
					return errors.New("upstream disconnected")
				}
				return nil
			})
			if _, err := perform(m, request(nil), next); err == nil {
				t.Fatal("expected fill error")
			}
			if len(files(t, m)) != 0 {
				t.Fatal("partial response published")
			}
		})
	}
}

func TestFinalHeadersAndBypass(t *testing.T) {
	m := newTestCache(t)
	var calls int
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls++
		w.WriteHeader(103)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(200)
		_, err := io.WriteString(w, "final")
		return err
	})
	for i := 0; i < 2; i++ {
		w, err := perform(m, request(nil), next)
		if err != nil || w.Code != 200 || w.Body.String() != "final" {
			t.Fatalf("final headers: %+v %v", w, err)
		}
	}
	if calls != 1 {
		t.Fatal("informational response prevented caching")
	}
	for _, h := range []http.Header{{"Authorization": {"Bearer test"}}, {"Range": {"bytes=0-1"}}, {"Cookie": {"nocache=1"}}, {"X-Nocache": {"1"}}} {
		count := 0
		next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error { count++; return nil })
		for i := 0; i < 2; i++ {
			if _, err := perform(m, request(h), next); err != nil {
				t.Fatal(err)
			}
		}
		if count != 2 {
			t.Fatalf("bypass failed for %v", h)
		}
	}
}

func TestDynamicStoragePaths(t *testing.T) {
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	defer cancel()
	base := t.TempDir()
	t.Setenv("CACHE_TEST_DIR", base)
	m := newTestCache(t)
	m.StoragePath = "{env.CACHE_TEST_DIR}/{http.vars.root_name}/{http.request.host}"
	m.Key, m.Inactive = "same-key", caddy.Duration(time.Hour)
	if err := m.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	started, release := make(chan struct{}, 4), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return r.Context().Err()
		}
		_, err := fmt.Fprintf(w, "%s/%s", caddyhttp.GetVar(r.Context(), "root_name"), r.Host)
		return err
	})
	makeRequest := func(app, host string) *http.Request {
		r := request(nil)
		r.Host = host
		caddyhttp.SetVar(r.Context(), "root_name", app)
		return r
	}
	results := make(chan error, 4)
	for _, app := range []string{"app-a", "app-b"} {
		for _, host := range []string{"a.test", "b.test"} {
			go func() {
				w, err := perform(m, makeRequest(app, host), next)
				if err == nil && w.Body.String() != app+"/"+host {
					err = fmt.Errorf("mixed storage: %q", w.Body.String())
				}
				results <- err
			}()
		}
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("different storage paths shared a fill")
		}
	}
	// All four fills must reach the upstream before any one of them completes.
	for range 4 {
		release <- struct{}{}
	}
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for _, app := range []string{"app-a", "app-b"} {
		for _, host := range []string{"a.test", "b.test"} {
			w, err := perform(m, makeRequest(app, host), next)
			if err != nil || w.Body.String() != app+"/"+host {
				t.Fatalf("hit: %q %v", w.Body.String(), err)
			}
			entries, err := filepath.Glob(filepath.Join(base, app, host, U.Md5("/test/public"), "*"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("entries: %v %v", entries, err)
			}
			old := time.Now().Add(-2 * time.Hour)
			if err := os.Chtimes(entries[0], old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("upstream calls: %d", calls.Load())
	}
	m.cleanCache()
	check := newTestCache(t)
	check.cache_dir = base
	if paths := files(t, check); len(paths) != 0 {
		t.Fatalf("dynamic cleanup left files: %v", paths)
	}

	for _, value := range []string{"", "missing", base} {
		x := newTestCache(t)
		x.cache_dir = "{http.vars.cache_dir}"
		r := request(nil)
		if value != "missing" {
			caddyhttp.SetVar(r.Context(), "cache_dir", value)
		}
		var reached bool
		_, err := perform(x, r, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			reached = true
			_, err := io.WriteString(w, "absolute path")
			return err
		}))
		if value == base {
			if err != nil || !reached || len(files(t, check)) != 1 {
				t.Fatalf("absolute variable path: %v", err)
			}
		} else if err == nil || reached {
			t.Fatalf("accepted empty or unknown storage variable: %q", value)
		}
	}
}

func TestStorageAndCELConfiguration(t *testing.T) {
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	defer cancel()
	t.Setenv("CACHE_TEST_DIR", t.TempDir())
	m := newTestCache(t)
	config := "proxy_cache {\n methods GET HEAD POST\n storage_path {env.CACHE_TEST_DIR}\n key {http.request.host}/{http.vars.root_name}\n bypass {http.request.header.X-Skip} == \"yes\"\n inactive 10m\n max_age 0\n wait_timeout 600s\n}"
	if err := m.UnmarshalCaddyfile(caddyfile.NewTestDispenser(config)); err != nil {
		t.Fatal(err)
	}
	if err := m.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()
	if m.cache_dir != os.Getenv("CACHE_TEST_DIR") || m.MaxAge == nil || *m.MaxAge != 0 || *m.WaitTimeout != caddy.Duration(600*time.Second) || m.Inactive != caddy.Duration(10*time.Minute) {
		t.Fatalf("configuration: %+v", m)
	}
	calls := 0
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls++
		_, err := fmt.Fprint(w, calls)
		return err
	})
	for _, tc := range []struct{ skip, want string }{{"", "1"}, {"yes", "2"}, {"", "1"}} {
		w, err := perform(m, request(http.Header{"X-Skip": {tc.skip}}), next)
		if err != nil || w.Body.String() != tc.want {
			t.Fatalf("CEL bypass: body=%q err=%v", w.Body.String(), err)
		}
	}
	if len(files(t, m)) != 1 {
		t.Fatal("bypassed response was stored")
	}
	for _, expr := range []string{"not valid CEL ?", "42"} {
		x := newTestCache(t)
		x.StoragePath = x.cache_dir
		x.Bypass = &caddyhttp.MatchExpression{Expr: expr}
		if err := x.Provision(ctx); err == nil {
			x.Cleanup()
			t.Fatalf("accepted expression %q", expr)
		}
	}
	x := newTestCache(t)
	x.StoragePath = x.cache_dir
	x.Bypass = &caddyhttp.MatchExpression{Expr: `int({http.request.header.Number}) > 0`}
	if err := x.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	defer x.Cleanup()
	if _, err := perform(x, request(nil), next); err == nil {
		t.Fatal("ignored CEL evaluation error")
	}
	for _, option := range []string{"wait_timeout 0", "inactive -1s", "max_age -1s", "key one two", "storage_path one two", "methods"} {
		if err := new(ProxyCache).UnmarshalCaddyfile(caddyfile.NewTestDispenser("proxy_cache {\n" + option + "\n}")); err == nil {
			t.Fatalf("accepted %q", option)
		}
	}
}

func bodyRequest(method, body string) *http.Request {
	r := request(nil)
	r.Method, r.Body, r.ContentLength = method, io.NopCloser(strings.NewReader(body)), int64(len(body))
	return r
}

func TestMethodsBodiesAndCustomKey(t *testing.T) {
	m := newTestCache(t)
	m.Methods = []string{"POST"}
	calls := 0
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "%s/%s", r.Method, body)
		return err
	})
	for _, body := range []string{"a", "b", "a", "b"} {
		w, err := perform(m, bodyRequest("POST", body), next)
		if err != nil || w.Body.String() != "POST/"+body {
			t.Fatalf("request body: %q %v", w.Body.String(), err)
		}
	}
	if calls != 2 {
		t.Fatalf("body variants: calls=%d", calls)
	}
	for i := 0; i < 2; i++ {
		if _, err := perform(m, bodyRequest("GET", ""), next); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 4 {
		t.Fatal("methods did not exclude GET")
	}
	m = newTestCache(t)
	m.Key = "{http.vars.tenant}/{http.request.uri.path}"
	calls = 0
	next = caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		calls++
		_, err := fmt.Fprint(w, calls)
		return err
	})
	for _, tc := range []struct{ tenant, query, header, want string }{{"one", "a=1", "en", "1"}, {"one", "a=2", "it", "1"}, {"two", "a=1", "en", "2"}} {
		r := request(http.Header{"Accept-Language": {tc.header}})
		r.URL.RawQuery = tc.query
		caddyhttp.SetVar(r.Context(), "tenant", tc.tenant)
		w, err := perform(m, r, next)
		if err != nil || w.Body.String() != tc.want {
			t.Fatalf("custom key: %q %v", w.Body.String(), err)
		}
	}
	m.Key = "{unknown.placeholder}"
	if _, err := perform(m, request(nil), next); err == nil {
		t.Fatal("unknown placeholder silently collapsed key")
	}
}

func TestRetentionAndAccessTime(t *testing.T) {
	for _, mode := range []string{"read", "cleanup"} {
		for _, tc := range []struct {
			name                              string
			idle, maxAge, birthAge, accessAge time.Duration
			retained                          bool
		}{
			{"idle-expired", 10 * time.Minute, 2 * time.Hour, time.Hour, 20 * time.Minute, false},
			{"absolute-expired", 10 * time.Minute, time.Hour, 2 * time.Hour, time.Second, false},
			{"access-extends-retention", 10 * time.Minute, 0, 48 * time.Hour, time.Minute, true},
			{"no-touch", 0, time.Hour, 5 * time.Minute, 2 * time.Hour, true},
			{"both-disabled", 0, 0, 48 * time.Hour, 48 * time.Hour, true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				m := newTestCache(t)
				m.Inactive = caddy.Duration(tc.idle)
				age := caddy.Duration(tc.maxAge)
				m.MaxAge = &age
				calls := 0
				next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
					calls++
					_, err := fmt.Fprint(w, calls)
					return err
				})
				if _, err := perform(m, request(nil), next); err != nil {
					t.Fatal(err)
				}
				path := files(t, m)[0]
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				parts := strings.SplitN(string(data), " ", 5)
				parts[1] = strconv.FormatInt(time.Now().Add(-tc.birthAge).UnixNano(), 10)
				if err := os.WriteFile(path, []byte(strings.Join(parts, " ")), 0600); err != nil {
					t.Fatal(err)
				}
				access := time.Now().Add(-tc.accessAge)
				if err := os.Chtimes(path, access, access); err != nil {
					t.Fatal(err)
				}
				before, _ := os.Stat(path)
				if mode == "cleanup" {
					m.cleanCache()
					if (len(files(t, m)) == 1) != tc.retained {
						t.Fatal("incorrect retention cleanup")
					}
					return
				}
				w, err := perform(m, request(nil), next)
				if err != nil {
					t.Fatal(err)
				}
				want := "2"
				if tc.retained {
					want = "1"
				}
				if w.Body.String() != want {
					t.Fatalf("retention lookup: %q", w.Body.String())
				}
				if !tc.retained {
					return
				}
				after, _ := os.Stat(path)
				if tc.idle == 0 && !after.ModTime().Equal(before.ModTime()) {
					t.Fatal("mtime changed with inactive disabled")
				}
				if tc.idle > 0 && !after.ModTime().After(before.ModTime()) {
					t.Fatal("last access was not updated")
				}
				seconds, _ := strconv.ParseInt(w.Header().Get("Age"), 10, 64)
				if seconds < int64(tc.birthAge/time.Second) {
					t.Fatalf("Age reset by access: %d", seconds)
				}
			})
		}
	}
}

func TestWaitTimeoutPreservesBodyAndCleansPrivateFill(t *testing.T) {
	m := newTestCache(t)
	m.Methods = []string{"POST"}
	timeout := caddy.Duration(30 * time.Millisecond)
	m.WaitTimeout = &timeout
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
			w.Header().Set("Set-Cookie", "private=1")
		}
		_, err = fmt.Fprintf(w, "body=%s", body)
		return err
	})
	results := make(chan error, 2)
	run := func() {
		w, err := perform(m, bodyRequest("POST", "payload"), next)
		if err == nil && w.Body.String() != "body=payload" {
			err = fmt.Errorf("lost body: %q", w.Body.String())
		}
		results <- err
	}
	go run()
	<-started
	go run()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("configured wait timeout ignored")
		}
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(files(t, m)) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(files(t, m)) != 0 || calls.Load() != 3 {
		t.Fatalf("fill leaked or fallback missing: files=%v calls=%d", files(t, m), calls.Load())
	}
}
