package proxy_cache

import "net/http"
import "io"
import "io/fs"
//import "io/ioutil"
import "slices"
import "fmt"
//import "sync"
import "crypto/md5"
import "encoding/hex"
import "path/filepath"
import "time"
import "os"
import "bufio"
import "strconv"
import "strings"

import "golang.org/x/sync/singleflight"

import "github.com/caddyserver/caddy/v2"
import "github.com/caddyserver/caddy/v2/modules/caddyhttp"
import "github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
import "github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"

import "go.uber.org/zap"

//import "github.com/puzpuzpuz/xsync/v3"

/*

https://stackoverflow.com/questions/43021058/golang-read-request-body-multiple-times
https://caddy.community/t/dynamically-set-reverse-proxy-upstreams-from-custom-module/10142/12

// fastcgi_cache_path {{model.web_cache_path?:'/var/cache/nginx'|raw}} keys_zone=cache_{{model.app_name|raw}}:{{model.web_cache_size?:'100m'|raw}} inactive=720h;
// fastcgi_cache cache_{{model.app_name|raw}};
// fastcgi_cache_bypass $cookie_nocache $http_x_nocache $arg_nocache;
// fastcgi_no_cache $cookie_nocache $http_x_nocache $arg_nocache;
// fastcgi_cache_key "$scheme$request_method$host$request_uri$http_authorization";
// fastcgi_cache_methods GET HEAD;
// fastcgi_cache_min_uses 1;
// fastcgi_cache_revalidate off;
// fastcgi_cache_use_stale timeout updating;
// fastcgi_cache_background_update on;
// fastcgi_cache_valid 200 404 301 302 303 307 {{model.web_cache_ttl?:'5m'|raw}};
// fastcgi_ignore_headers Cache-Control Expires Set-Cookie;
// fastcgi_cache_lock on;
// fastcgi_cache_lock_age 600s;
// fastcgi_cache_lock_timeout 600s;
// #fastcgi_cache_max_range_offset 1024;
// fastcgi_store_access user:rw group:rw;

type SyncedMap[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

func (m *SyncedMap[K, V]) Get(key K) V {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.m[key]
}

func (m *SyncedMap[K, V]) Set(key K, value V) {
	m.mu.Lock()
	m.m[key] = value
	m.mu.Unlock()
}

*/

//--------------------------------------------------------------------------------
//static call like: UT{}.Method() or U.Method()
type ut struct {}
var U ut

func (ut) Md5(input string) string {
   hash := md5.Sum([]byte(input))
   ret := hex.EncodeToString(hash[:])
   return ret
}

// //type Header map[string][]string
// func (ut) WriteHeader(fd io.Writer,header http.Header,skip []string) {
// 	return
// }

// func (ut) LoadHeader(fd io.Reader) http.Header {
// 	return nil
// }

//--------------------------------------------------------------------------------

// https://github.com/caddyserver/caddy/blob/master/modules/caddyhttp/responsewriter.go
// https://stackoverflow.com/questions/54086270/get-raw-header-of-http-response
// https://gist.github.com/ismasan/d03d602b8e4e37862547e9a6f0391dc9
type FileResponseWriter struct {
	http.ResponseWriter
	header http.Header
	wroteHeader bool
	bodySize int
	statusCode int
	w io.Writer
	key string
}

func NewFileResponseWriter(key string,w io.Writer) *FileResponseWriter {
	o := new(FileResponseWriter)
	o.header = make(http.Header)
	o.w = w
	o.key = key
	return o
}

func (frw *FileResponseWriter) Header() http.Header {
	return frw.header
}

func (frw *FileResponseWriter) Write(p []byte) (n int, err error) {
	//fmt.Fprintf(os.Stderr, "Write: %s\n", p)
	if !frw.wroteHeader {
		frw.WriteHeader(http.StatusOK)
	}
	nw,err := frw.w.Write(p)
	frw.bodySize += nw
	return nw,err
}

func (frw *FileResponseWriter) WriteHeader(statusCode int) {
	if frw.wroteHeader {
		return
	}
	frw.wroteHeader = true
	frw.statusCode = statusCode
	fmt.Fprintf(frw.w, "%s\n", frw.key)
	//fmt.Fprintf(os.Stderr, "WriteHeader: %d\n", statusCode)
	for name, values := range frw.Header() {
		for _, v := range values {
			//fmt.Fprintf(os.Stderr, "WriteHeader: %s: %s\n", name,v)
    	fmt.Fprintf(frw.w, "%s: %s\n", name,v)
  	}
  }
  fmt.Fprintf(frw.w, "%d\n", statusCode)
  fmt.Fprintf(frw.w, "\n")
}

func (frw *FileResponseWriter) IsOk() bool {
	//return frw.wroteHeader && frw.bodySize>0
	return frw.wroteHeader
}

/*
func (rww *ResponseWriterWrapper) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := rww.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return ErrNotImplemented
}

// ReadFrom implements io.ReaderFrom. It simply calls io.Copy,
// which uses io.ReaderFrom if available.
func (rww *ResponseWriterWrapper) ReadFrom(r io.Reader) (n int64, err error) {
	return io.Copy(rww.ResponseWriter, r)
}
*/
//--------------------------------------------------------------------------------

type ProxyCache struct {
	logger *zap.Logger
	sfg *singleflight.Group
	cache_dir string
	clean_timer *time.Timer
}

//----------------------------------------------------------------------------------------

func NewProxyCache() *ProxyCache {
	m := new(ProxyCache)
	m.sfg = new(singleflight.Group)
	//m.ReadyLockMap = xsync.NewMapOf[string, string]()
	return m
}

func init() {
	caddy.RegisterModule(ProxyCache{})
	httpcaddyfile.RegisterHandlerDirective("proxy_cache", parseCaddyfile)
}

func (ProxyCache) CaddyModule() caddy.ModuleInfo {
	mi := caddy.ModuleInfo{
		ID:  "http.handlers.proxy_cache",
		New: func() caddy.Module {
			return NewProxyCache()
		},
	}
	return mi
}

func (m *ProxyCache) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger()
	//defer m.logger.Sync()
	//var log = m.logger.Sugar()
	//log.Debugln("Provision")
	m.cache_dir = filepath.Join(caddy.AppDataDir(),"proxy_cache")
	//os.MkdirAll(m.cache_dir, 0644) //@error handling
	go m.cleanCache()
	return nil
}

func (m *ProxyCache) Cleanup() error {
	if m.clean_timer != nil {
		m.clean_timer.Stop()
	}
	return nil
}

func (m *ProxyCache) cleanCache() {
	defer m.logger.Sync()
	var log = m.logger.Sugar()

	var clean_timer_sec = 30
	var max_files_delete = 200
	var max_file_ttl_mins = 60
	
	var num_del = 0
	var now = time.Now()
	filepath.WalkDir(m.cache_dir, func (s string, f fs.DirEntry, err error) error {
		if err != nil {
		  return nil
		}
		if f.IsDir() {
			var ff, _ = os.Open(s)
			if ff != nil {
				var files, _ = ff.Readdir(3)
				if len(files)<=2 {
					log.Debugln("clean cache dir: ",s)
					os.Remove(s)
				}
			}
		}
		if !f.Type().IsRegular() {
			return nil
		}
		var finfo, _ = f.Info()
		if finfo == nil {
			return nil
		}
		var exptime = finfo.ModTime().Add(time.Duration(max_file_ttl_mins) * time.Minute)
		var is_expired = now.After(exptime)
    if !is_expired {
    	return nil
    }
    log.Debugln("clean cache file: ",s)
    os.Remove(s)
		num_del += 1
		if num_del >= max_files_delete {
			return fs.SkipAll
		}
		return nil
	})
	
	m.clean_timer = time.AfterFunc(time.Duration(clean_timer_sec) * time.Second, m.cleanCache)
}

//------------------------------------------------------------------------------

func (m *ProxyCache) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // consume directive name
	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m ProxyCache
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return m, err
}

//----------------------------------------------------------------------------------------

/*type ProxyCache struct {
	logger *zap.Logger
}*/

func (m ProxyCache) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	defer m.logger.Sync()
	var log = m.logger.Sugar()
	
	var nocache_arg_name = "nocache"
	var cacheable_methods = []string{"GET","HEAD"}
	//var cacheable_resp_status_range = []int{200,499}
  var cacheable_resp_ignore_headers = []string{"Cache-Control", "Expires", "Set-Cookie"}
  var cache_ttl = 300
  var cache_max_wait = 300
  var fperm = os.FileMode(0700)

  var req_useragent = r.UserAgent()
  var req_remoteaddr = r.RemoteAddr

	var req_tls = "plain"
	if r.TLS != nil {
		req_tls = "tls"
	}
	var req_method = r.Method
	var req_proto = r.Proto //r.URL.Scheme
	var req_host = r.Host //r.URL.Host
	//log.Debugln("r.URL.RawPath: ",r.URL.RawQuery)
	//log.Debugln("r.URL.RawQuery: ",r.URL.RawQuery)
	//log.Debugln("r.RequestURI: ",r.RequestURI)
	//log.Debugln("r.URL.String(): ",r.URL.String())
	//var req_path = r.URL.RawPath
	//var req_query = r.URL.RawQuery
	//var req_uri = r.RequestURI
	orig_req := r.Context().Value(caddyhttp.OriginalRequestCtxKey).(http.Request)
	var req_uri = orig_req.URL.RequestURI()

	//var req_uri = r.URL.String()
	var docroot = caddyhttp.GetVar(r.Context(),"root").(string)
	var docroot_md5 = U.Md5(docroot)
  var is_cacheable_method = slices.Index(cacheable_methods,r.Method) != -1
  //var nocache_arg = r.FormValue(nocache_arg_name) //HUGE WARN!!!! read the request body, and fuck up the request for subsequent middlewares so avoid it and use only GET arguments, see https://stackoverflow.com/questions/43021058/golang-read-request-body-multiple-times
  var nocache_arg = r.URL.Query().Get(nocache_arg_name)
  var nocache_cookie_value = ""
  var nocache_cookie, _ = r.Cookie(nocache_arg_name)
  if nocache_cookie != nil {
  	nocache_cookie_value = nocache_cookie.Value
  }
  var auth_header = r.Header.Get("Authorization")
  var range_header = r.Header.Get("Range")
  var nocache_header = r.Header.Get("X-NoCache")
  var cache_fmt = "docroot: %v req_method: %v req_tls: %v req_proto: %v req_host: %v req_uri: %v auth_header: %v"
  //var cache_fmt = "%v|%v|%v|%v|%v|%v|%v"
  var cache_key = fmt.Sprintf(cache_fmt,docroot,req_method,req_tls,req_proto,req_host,req_uri,auth_header)
  var cache_key_md5 = U.Md5(cache_key)
  var cache_dir = filepath.Join(m.cache_dir,docroot_md5)
  var cache_path = filepath.Join(cache_dir,cache_key_md5)
  var is_req_cacheable = is_cacheable_method && auth_header=="" && range_header=="" && nocache_header!="1" && nocache_arg!="1" && nocache_cookie_value!="1"
  
	log.Debugln("nocache_arg_name: ",nocache_arg_name)
	log.Debugln("cacheable_methods: ",cacheable_methods)
	//log.Debugln("cacheable_resp_status_range: ",cacheable_resp_status_range)
	log.Debugln("cacheable_resp_ignore_headers: ",cacheable_resp_ignore_headers)
	log.Debugln("cache_ttl: ",cache_ttl)
	log.Debugln("cache_max_wait: ",cache_max_wait)

  log.Debugln("req_method: ",req_method)
  log.Debugln("req_tls: ",req_tls)
  log.Debugln("req_proto: ",req_proto)
  log.Debugln("req_host: ",req_host)
  log.Debugln("req_uri: ",req_uri)
  log.Debugln("docroot: ",docroot)
  log.Debugln("docroot_md5: ",docroot_md5)
  log.Debugln("is_cacheable_method: ",is_cacheable_method)
  log.Debugln("nocache_cookie_value: ",nocache_cookie_value)
  log.Debugln("nocache_arg: ",nocache_arg)
  log.Debugln("auth_header: ",auth_header)
  log.Debugln("range_header: ",range_header)
  log.Debugln("nocache_header: ",nocache_header)
  log.Debugln("cache_key: ",cache_key)
  log.Debugln("cache_key_md5: ",cache_key_md5)
  log.Debugln("cache_dir: ",cache_dir)
  log.Debugln("cache_path: ",cache_path)
  log.Debugln("is_req_cacheable: ",is_req_cacheable)
  log.Debugln("---")
  
  
  if !is_req_cacheable {
  	return next.ServeHTTP(w,r)
  } else {
  	var wu = time.Now().Add(time.Duration(cache_max_wait) * time.Second)
	  for true {
	  	var now = time.Now()
		  var is_max_wait_elapsed = now.After(wu)
		  log.Debugln("now: ",now.Format(time.DateTime))
		  log.Debugln("cache_max_wait_until: ",wu.Format(time.DateTime))
		  log.Debugln("is_max_wait_elapsed: ",is_max_wait_elapsed)
		  if is_max_wait_elapsed {
		  	return next.ServeHTTP(w,r)
		  }

		  cache_item_fd, _ := os.Open(cache_path)
		  defer cache_item_fd.Close()
		  cache_stat, _ := cache_item_fd.Stat()
		  var is_cache_item_valid = cache_stat!=nil && cache_stat.Size()>0
		  var cache_exptime = time.Time{}
		  var is_cache_item_stale = false
		  if cache_stat != nil {
		  	cache_exptime = cache_stat.ModTime().Add(time.Duration(cache_ttl) * time.Second)
		  	is_cache_item_stale = is_cache_item_valid && now.After(cache_exptime)
		  }
		  var is_cache_item_to_update = !is_cache_item_valid || is_cache_item_stale
		  
		  log.Debugln("is_cache_item_valid: ",is_cache_item_valid)
		  log.Debugln("now: ",now.Format(time.DateTime))
		  log.Debugln("cache_exptime: ",cache_exptime.Format(time.DateTime))
		  log.Debugln("is_cache_item_stale: ",is_cache_item_stale)
		  log.Debugln("is_cache_item_to_update: ",is_cache_item_to_update)
		  log.Debugln("---")

		  var sfc <-chan singleflight.Result
		  if is_cache_item_to_update {
			  sfc = m.sfg.DoChan(cache_key_md5,func() (interface{}, error) {
			  	os.MkdirAll(cache_dir, fperm)
			  	//ioutil.WriteFile(cache_path, []byte(cache_key), 0644)
			  	var cache_path_tmp = fmt.Sprintf("%s.tmp",cache_path)
			  	tmp_file_fd, _ := os.OpenFile(cache_path_tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fperm)
			  	var cache_item_key = fmt.Sprintf("time: %s %s req_useragent: %s req_remoteaddr: %s",time.Now().Format(time.DateTime),cache_key,req_useragent,req_remoteaddr)
			  	fw := NewFileResponseWriter(cache_item_key,tmp_file_fd)
			  	defer func() {
			  		log.Debugln("cache_key: ",cache_key)
			  		tmp_file_fd.Close()
				  	if fw.IsOk() {
					  	log.Debugln("remove: ",cache_path)
					  	os.Remove(cache_path)
					  	log.Debugln("rename: ",cache_path_tmp,cache_path)
					  	os.Rename(cache_path_tmp,cache_path)
					  } else {
					  	log.Debugln("remove invalid tmp: ",cache_path_tmp)
					  	os.Remove(cache_path_tmp)
					  }
			  	}()
			  	var next_err = next.ServeHTTP(fw,r)
				  log.Debugln("---")
					return true, next_err
				})
				//log.Debugln("sfc: ",sfc)
			}

			if sfc!=nil && !is_cache_item_valid {
				select {
					case res := <-sfc:
						if res.Err != nil {
							log.Debugln("error from singleflight next.ServeHTTP(): %v",res.Err)
							return res.Err
						}
					case <-time.After(time.Until(wu)):
				}
				continue
			}

			//d,_ := ioutil.ReadFile(cache_path)
			//w.Write(d)
			//io.Copy(w,cache_item_file)
			br := bufio.NewReader(cache_item_fd)
			cache_item_key, _ := br.ReadString('\n')
			log.Debugln("start serve cached item: ",cache_key_md5)
			log.Debugln("cache_item_key: ",cache_item_key)
			log.Debugln("cache_path: ",cache_path)
			statusCode := 0
			for true {
				line, err_read := br.ReadString('\n')
				ll := len(line)
				if ll>=1 {
					line = line[:ll-1]
				}
				//log.Debugln("resp file line: ",line)
				if(len(line)==0 || err_read!=nil) {
					w.WriteHeader(statusCode)
					break
				}
				toks := strings.SplitN(line,": ",2)
				if len(toks)<2 {
					statusCode, _ = strconv.Atoi(line)
				} else {
					//@todo normalize header names?
					if slices.Index(cacheable_resp_ignore_headers,toks[0]) == -1 {
						w.Header().Add(toks[0],toks[1])
					}
				}
			}
			io.Copy(w,br)
			log.Debugln("end serve cached item: ",cache_key_md5)
			break
		}
	}

  return nil

  /*
	while(1) {
		//check cache ttl
		var cache_is_stale = now>file_created+cache_ttl

		//if cache is stale
		if cache_is_stale {
			//check if some request is already going upstream
			lock_ok := LockWaitingUpstream.trylock(cache_key)
			//we are the one that call upstream
			if lock_ok {
				//redirect upstream resp to client and "save to cache eventually" then unlock
				return ServeAndEventuallyCacheHTTP(next.ServeHTTP(w,r),lock_ok,cache_resp_status,cache_resp_ignore_headers)
			}
		}

		//wait new cache file for key to be switched because should be fast
	  LockSwitching.waitifkeyexists(cache_key)

		//open cache item
		var cache_fd = open_for_read(cache_path)
	  
		//serve cache item if exists
		if cache_fd {
			return ServeHTTP(cache_fd)
		}
		//eventually wait for who is calling upstream
		else {
			LockWaitingUpstream.wait(cache_key)
		}
	}
	*/


}




//-----------------------------------------------------------------------------

var (
	_ caddyhttp.MiddlewareHandler = (*ProxyCache)(nil)
	_ caddy.Provisioner           = (*ProxyCache)(nil)
	_ caddy.CleanerUpper          = (*ProxyCache)(nil)
	_ caddyfile.Unmarshaler       = (*ProxyCache)(nil)

	_ http.ResponseWriter = (*FileResponseWriter)(nil)
)