// Package nas serves a Tash folder over authenticated WebDAV.
package nas

import (
	"context"
	"crypto/subtle"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tash/tash/internal/config"
	"github.com/tash/tash/internal/tailscale"
	"golang.org/x/net/webdav"
)

// Server is a WebDAV file server.
type Server struct {
	cfg config.NASConfig

	mu      sync.RWMutex
	httpSrv *http.Server
	ln      net.Listener
	url     string
	err     string
	started time.Time
	reqs    atomic.Int64
}

// New creates a server (not started).
func New(cfg config.NASConfig) *Server { return &Server{cfg: cfg} }

// URL returns the advertised base URL.
func (s *Server) URL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.url
}

// DAVURL returns the WebDAV mount URL.
func (s *Server) DAVURL() string {
	u := s.URL()
	if u == "" {
		return ""
	}
	return u + "/dav/"
}

// Root returns the exposed folder.
func (s *Server) Root() string { return s.cfg.Root }

// Running reports whether the listener is active.
func (s *Server) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ln != nil
}

// Err returns the last listener error.
func (s *Server) Err() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// Requests returns the number of served HTTP requests.
func (s *Server) Requests() int64 { return s.reqs.Load() }

// Start creates the root folder and binds the NAS port.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.ln != nil {
		s.mu.Unlock()
		return nil
	}
	cfg := s.cfg
	if cfg.User == "" || cfg.Password == "" {
		s.mu.Unlock()
		return fmt.Errorf("NAS username and password are required")
	}
	if cfg.Root == "" {
		s.mu.Unlock()
		return fmt.Errorf("NAS root folder is empty")
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("create NAS folder: %w", err)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("NAS port %d: %w", cfg.Port, err)
	}
	handler := s.routes(cfg)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	s.ln = ln
	s.httpSrv = srv
	s.started = time.Now()
	s.url = fmt.Sprintf("http://%s:%d", advertiseHost(), cfg.Port)
	s.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.mu.Lock()
			s.err = err.Error()
			s.mu.Unlock()
		}
	}()
	return nil
}

// Stop gracefully stops the server.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	srv := s.httpSrv
	ln := s.ln
	s.httpSrv = nil
	s.ln = nil
	s.url = ""
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// Restart applies new credentials/settings.
func (s *Server) Restart(cfg config.NASConfig, ctx context.Context) error {
	_ = s.Stop(ctx)
	s.mu.Lock()
	s.cfg = cfg
	s.err = ""
	s.mu.Unlock()
	return s.Start()
}

func (s *Server) routes(cfg config.NASConfig) http.Handler {
	dav := &webdav.Handler{
		Prefix:     "/dav",
		FileSystem: webdav.Dir(cfg.Root),
		LockSystem: webdav.NewMemLS(),
		Logger:     func(*http.Request, error) {},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			s.serveIndex(w, r, cfg)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/browse") {
			s.serveBrowse(w, r, cfg)
			return
		}
		http.NotFound(w, r)
	})
	mux.Handle("/dav/", s.readOnlyGuard(cfg.ReadOnly, dav))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"service":"tash-nas"}`+"\n")
	})
	return s.instrument(s.auth(mux, cfg))
}

func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reqs.Add(1)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler, cfg config.NASConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(cfg.User)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Tash NAS", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) readOnlyGuard(readOnly bool, next http.Handler) http.Handler {
	if !readOnly {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "NAS is mounted read-only", http.StatusForbidden)
		}
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, cfg config.NASConfig) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	host := r.Host
	davURL := "http://" + host + "/dav/"
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Tash NAS</title><style>
:root{color-scheme:dark}body{background:#050b08;color:#c8ffd6;font-family:ui-monospace,Menlo,Consolas,monospace;margin:0;padding:2rem;line-height:1.5}
h1{color:#00ff66;letter-spacing:.15em;margin:0 0 .3rem;text-shadow:0 0 12px #00ff6655}
a{color:#b6ff00;text-decoration:none}a:hover{text-decoration:underline}
.card{border:1px solid #00a844;background:#07140d;padding:1rem 1.2rem;margin:1rem 0;border-radius:4px;max-width:800px}
code{background:#0b2417;padding:.15rem .35rem;border-radius:3px}.muted{color:#3f6b50}.row{display:flex;justify-content:space-between;gap:1rem;border-bottom:1px dotted #123d26;padding:.35rem 0}
</style></head><body><h1>◢ TASH NAS ◣</h1><div class="muted">authenticated WebDAV vault</div>
<div class="card"><strong>Mount URL</strong><br><code>%s</code><br><span class="muted">user: <code>%s</code> · folder: <code>%s</code></span></div>
<div class="card"><strong>Connect</strong><div class="row"><span>Windows Explorer</span><span class="muted">Map Network Drive → %s</span></div>
<div class="row"><span>macOS Finder</span><span class="muted">Go → Connect to Server → %s</span></div>
<div class="row"><span>Linux</span><span class="muted">davfs2 / gvfs: %s</span></div>
<div class="row"><span>Android</span><span class="muted">Solid Explorer, RCX, Material Files → WebDAV</span></div></div>
<div class="card"><strong>Files</strong><br>`, html.EscapeString(davURL), html.EscapeString(cfg.User), html.EscapeString(cfg.Root),
		html.EscapeString("http://"+host+"/dav/"), html.EscapeString("http://"+host+"/dav/"), html.EscapeString("http://"+host+"/dav/"))
	entries, err := os.ReadDir(cfg.Root)
	if err != nil {
		fmt.Fprintf(w, `<span class="muted">cannot read folder: %s</span>`, html.EscapeString(err.Error()))
	} else if len(entries) == 0 {
		fmt.Fprint(w, `<span class="muted">empty folder — upload via WebDAV or the Tash editor</span>`)
	} else {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
		})
		for i, e := range entries {
			if i >= 100 {
				break
			}
			icon := "·"
			if e.IsDir() {
				icon = "▸"
			}
			fmt.Fprintf(w, `<div class="row"><span>%s <a href="/dav/%s">%s</a></span><span class="muted">%s</span></div>`,
				icon, html.EscapeString(strings.TrimPrefix(filepath.ToSlash(filepath.Join("/", e.Name())), "/")),
				html.EscapeString(e.Name()), formatSize(e))
		}
	}
	fmt.Fprint(w, `</div></body></html>`)
}

func (s *Server) serveBrowse(w http.ResponseWriter, r *http.Request, cfg config.NASConfig) {
	http.Redirect(w, r, "/", http.StatusFound)
}

func formatSize(e os.DirEntry) string {
	i, err := e.Info()
	if err != nil {
		return ""
	}
	if e.IsDir() {
		return "dir"
	}
	n := i.Size()
	switch {
	case n < 1024:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
	}
}

func advertiseHost() string {
	if ip := tailscale.LocalIPv4(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// MountHelp returns platform-specific connection hints.
func MountHelp(platform string) string {
	switch platform {
	case "windows":
		return "Explorer → This PC → Map network drive → folder: paste the WebDAV URL. Windows may require the WebClient service."
	case "darwin":
		return "Finder → Go → Connect to Server (⌘K) → paste the WebDAV URL."
	case "linux":
		return "GNOME Files: Other Locations → Connect to Server. CLI: mount.davfs URL /mnt/tash."
	default:
		return "Android: add a WebDAV storage in Solid Explorer / RCX / Material Files. Termux: use a WebDAV client or curl."
	}
}

// EnsureRoot creates the NAS directory if needed.
func EnsureRoot(root string) error { return os.MkdirAll(root, 0o700) }
