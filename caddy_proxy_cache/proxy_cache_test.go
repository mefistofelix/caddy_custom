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
