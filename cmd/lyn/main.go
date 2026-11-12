package main

import (
	"bytes"
	"fmt"
	"html"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/beint-no/lyn/build"
	"github.com/fsnotify/fsnotify"
)

const usage = `lyn builds a site into public/ and fails on anything broken or unused.

usage:
  lyn build [dir]   build once (incremental when .lyn/state exists)
  lyn serve [dir]   build, serve on http://localhost:1313 and rebuild on change
  lyn inspect [dir] print the configuration and pages as JSON for tools
  lyn links [dir]   build, then request every external link and fail on dead ones
`

func main() {
	debug.SetGCPercent(200)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(64)
	}
	dir := "."
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}
	if err := checkPinnedVersion(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "build", "links":
		b := build.NewBuilder(dir)
		b.Links = os.Args[1] == "links"
		res, err := b.Build()
		if res != nil {
			fmt.Println(res)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "inspect":
		out, err := build.Inspect(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Stdout.Write(out)
	case "serve":
		if err := serve(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(64)
	}
}

const reloadScript = `<script>new EventSource("/__lyn").onmessage=()=>location.reload()</script>`

type server struct {
	b       *build.Builder
	mu      sync.Mutex
	err     error
	clients map[chan struct{}]bool
}

func serve(dir string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	s := &server{b: build.NewBuilder(root), clients: map[chan struct{}]bool{}}
	s.rebuild()
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	for _, d := range []string{"content", "layouts", "assets", "static", "i18n", "data"} {
		filepath.WalkDir(filepath.Join(root, d), func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				w.Add(p)
			}
			return nil
		})
	}
	w.Add(root)
	go func() {
		var timer *time.Timer
		for ev := range w.Events {
			if strings.Contains(ev.Name, string(filepath.Separator)+".lyn") || strings.Contains(ev.Name, string(filepath.Separator)+"public") {
				continue
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					w.Add(ev.Name)
				}
			}
			s.b.Changed(ev.Name, ev.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0)
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(40*time.Millisecond, s.rebuild)
		}
	}()
	public := filepath.Join(root, "public")
	files := http.FileServer(http.Dir(public))
	mux := http.NewServeMux()
	mux.HandleFunc("/__lyn", s.events)
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		berr := s.err
		s.mu.Unlock()
		if berr != nil {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(rw, "<!doctype html><title>Build failed</title><pre style=\"white-space:pre-wrap;font:14px monospace;padding:2em\">%s</pre>%s", html.EscapeString(berr.Error()), reloadScript)
			return
		}
		p := r.URL.Path
		if strings.HasSuffix(p, "/") || strings.HasSuffix(p, ".html") {
			file := filepath.Join(public, filepath.FromSlash(p))
			if strings.HasSuffix(p, "/") {
				file = filepath.Join(file, "index.html")
			}
			data, err := os.ReadFile(file)
			status := http.StatusOK
			if err != nil {
				data, _ = os.ReadFile(filepath.Join(public, "404.html"))
				status = http.StatusNotFound
			}
			data = bytes.Replace(data, []byte("</body>"), []byte(reloadScript+"</body>"), 1)
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
			rw.WriteHeader(status)
			rw.Write(data)
			return
		}
		files.ServeHTTP(rw, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:1313")
	if err != nil {
		return err
	}
	fmt.Println("serving http://localhost:1313")
	return http.Serve(ln, mux)
}

func (s *server) rebuild() {
	res, err := s.b.Build()
	s.mu.Lock()
	s.err = err
	for c := range s.clients {
		select {
		case c <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
	if res != nil {
		fmt.Println(res)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func (s *server) events(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	c := make(chan struct{}, 1)
	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()
	flusher, _ := rw.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c:
			fmt.Fprint(rw, "data: reload\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func checkPinnedVersion(dir string) error {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return checkPin(dir, bi.Main.Version)
}

func checkPin(dir, version string) error {
	if !strings.HasPrefix(version, "v") {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	for d := abs; ; d = filepath.Dir(d) {
		if b, err := os.ReadFile(filepath.Join(d, ".lyn-version")); err == nil {
			want := strings.TrimSpace(string(b))
			if want != version {
				return fmt.Errorf("%s pins lyn %s but this is lyn %s; install it with:\n  go install github.com/beint-no/lyn/cmd/lyn@%s", filepath.Join(d, ".lyn-version"), want, version, want)
			}
			return nil
		}
		if filepath.Dir(d) == d {
			return nil
		}
	}
}
