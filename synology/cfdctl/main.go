// Command cfdctl is a tiny management helper for the cloudflared Synology
// package. It supervises the cloudflared tunnel process and serves a small
// web UI so the tunnel can be configured (paste a token), started, stopped
// and inspected from DSM without an SSH session.
//
// It only uses the Go standard library so it can be cross-compiled statically
// for old DSM 6.2.4 kernels.
package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/index.html
var indexHTML string

type supervisor struct {
	mu         sync.Mutex
	binary     string
	logPath    string
	pidPath    string
	tokenPath  string
	configPath string
	selfPID    string
	version    string
}

// tunnelArgs returns the arguments used to launch cloudflared, or an error
// when neither a tunnel token nor a config file is available.
func (s *supervisor) tunnelArgs() ([]string, error) {
	if b, err := os.ReadFile(s.tokenPath); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return []string{"tunnel", "--no-autoupdate", "run", "--token", tok}, nil
		}
	}
	if fi, err := os.Stat(s.configPath); err == nil && !fi.IsDir() {
		return []string{"tunnel", "--no-autoupdate", "--config", s.configPath, "run"}, nil
	}
	return nil, fmt.Errorf("尚未配置：请先填入 Tunnel Token")
}

func (s *supervisor) mode() string {
	if b, err := os.ReadFile(s.tokenPath); err == nil && strings.TrimSpace(string(b)) != "" {
		return "token"
	}
	if fi, err := os.Stat(s.configPath); err == nil && !fi.IsDir() {
		return "config"
	}
	return "none"
}

// currentPID reports the PID of a live cloudflared process, or 0.
func (s *supervisor) currentPID() int {
	b, err := os.ReadFile(s.pidPath)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return 0
	}
	return pid
}

func (s *supervisor) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentPID() > 0 {
		return nil
	}
	args, err := s.tunnelArgs()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	cmd := exec.Command(s.binary, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(s.pidPath, []byte(strconv.Itoa(pid)+"\n"), 0o644)
	go func() {
		_ = cmd.Wait()
		if b, err := os.ReadFile(s.pidPath); err == nil {
			if strings.TrimSpace(string(b)) == strconv.Itoa(pid) {
				_ = os.Remove(s.pidPath)
			}
		}
	}()

	time.Sleep(1500 * time.Millisecond)
	if s.currentPID() == 0 {
		return fmt.Errorf("cloudflared 启动后立即退出，请查看日志")
	}
	return nil
}

func (s *supervisor) stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid := s.currentPID()
	if pid == 0 {
		_ = os.Remove(s.pidPath)
		return nil
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	for i := 0; i < 30; i++ {
		if s.currentPID() == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if s.currentPID() != 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		time.Sleep(300 * time.Millisecond)
	}
	_ = os.Remove(s.pidPath)
	return nil
}

func (s *supervisor) restart() error {
	_ = s.stop()
	return s.start()
}

type status struct {
	Running    bool   `json:"running"`
	PID        int    `json:"pid"`
	Mode       string `json:"mode"`
	Configured bool   `json:"configured"`
	Version    string `json:"version"`
	Message    string `json:"message"`
}

func (s *supervisor) status() status {
	pid := s.currentPID()
	st := status{
		Running:    pid > 0,
		PID:        pid,
		Mode:       s.mode(),
		Configured: s.mode() != "none",
		Version:    s.version,
	}
	switch {
	case pid > 0:
		st.Message = "隧道运行中"
	case st.Configured:
		st.Message = "已配置，但当前未运行"
	default:
		st.Message = "未配置：请填入 Tunnel Token 后保存"
	}
	return st
}

func tail(path string, lines int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// authStore holds the management login (user:password) that the helper checks
// itself. DSM's own login is not involved: the credentials live in a plain file
// and simply gate the /api/ endpoints, so the UI is reachable without a DSM
// session.
type authStore struct {
	mu   sync.RWMutex
	path string
	user string
	pass string
}

// loadAuth reads "username:password" from path. It falls back to admin/admin
// when the file is missing or malformed so the UI is never left wide open by
// accident.
func loadAuth(path string) *authStore {
	a := &authStore{path: path, user: "admin", pass: "admin"}
	b, err := os.ReadFile(path)
	if err != nil {
		return a
	}
	line := strings.TrimSpace(string(b))
	if i := strings.IndexByte(line, ':'); i > 0 {
		a.user, a.pass = line[:i], line[i+1:]
	}
	return a
}

func (a *authStore) check(user, pass string) bool {
	a.mu.RLock()
	cu, cp := a.user, a.pass
	a.mu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(user), []byte(cu)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(cp)) == 1
}

// set persists new credentials and updates the live values.
func (a *authStore) set(user, pass string) error {
	if err := os.WriteFile(a.path, []byte(user+":"+pass+"\n"), 0o600); err != nil {
		return err
	}
	a.mu.Lock()
	a.user, a.pass = user, pass
	a.mu.Unlock()
	return nil
}

func main() {
	listen := flag.String("listen", "0.0.0.0:8321", "address to serve the management UI on")
	binary := flag.String("binary", "", "path to the cloudflared binary")
	vardir := flag.String("vardir", "", "writable package data directory")
	authfile := flag.String("authfile", "", "file containing the management login as user:password")
	flag.Parse()

	if *vardir == "" {
		*vardir = os.Getenv("SYNOPKG_PKGVAR")
	}
	if *vardir == "" {
		*vardir = "/var/packages/cloudflared/var"
	}
	if *binary == "" {
		*binary = filepath.Join(os.Getenv("SYNOPKG_PKGDEST"), "bin", "cloudflared")
	}
	if _, err := os.Stat(*binary); err != nil {
		*binary = "/var/packages/cloudflared/target/bin/cloudflared"
	}
	_ = os.MkdirAll(*vardir, 0o755)
	if *authfile == "" {
		*authfile = filepath.Join(*vardir, "auth")
	}
	auth := loadAuth(*authfile)

	s := &supervisor{
		binary:     *binary,
		logPath:    filepath.Join(*vardir, "cloudflared.log"),
		pidPath:    filepath.Join(*vardir, "cloudflared.pid"),
		tokenPath:  filepath.Join(*vardir, "tunnel-token"),
		configPath: filepath.Join(*vardir, "config.yml"),
		selfPID:    filepath.Join(*vardir, "cfdctl.pid"),
	}
	_ = os.WriteFile(s.selfPID, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)

	if out, err := exec.Command(s.binary, "--version").Output(); err == nil {
		s.version = strings.TrimSpace(string(out))
	}

	if s.mode() != "none" {
		if err := s.start(); err != nil {
			log.Printf("autostart: %v", err)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		_ = s.stop()
		_ = os.Remove(s.selfPID)
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, indexHTML)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.status())
	})
	mux.HandleFunc("/api/log", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		if n <= 0 || n > 2000 {
			n = 200
		}
		writeJSON(w, http.StatusOK, map[string]string{"log": tail(s.logPath, n)})
	})
	mux.HandleFunc("/api/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		tok := strings.TrimSpace(string(body))
		if tok == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token 不能为空"})
			return
		}
		if err := os.WriteFile(s.tokenPath, []byte(tok+"\n"), 0o600); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		err := s.restart()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": err == nil, "error": errString(err), "status": s.status()})
	})
	action := func(fn func() error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
				return
			}
			err := fn()
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": err == nil, "error": errString(err), "status": s.status()})
		}
	}
	mux.HandleFunc("/api/start", action(s.start))
	mux.HandleFunc("/api/stop", action(s.stop))
	mux.HandleFunc("/api/restart", action(s.restart))
	mux.HandleFunc("/api/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
			return
		}
		var in struct {
			User string `json:"user"`
			Pass string `json:"pass"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
			return
		}
		in.User = strings.TrimSpace(in.User)
		in.Pass = strings.TrimSpace(in.Pass)
		if in.User == "" || in.Pass == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "账号和密码不能为空"})
			return
		}
		if strings.ContainsAny(in.User, ":") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "账号不能包含冒号"})
			return
		}
		if err := auth.set(in.User, in.Pass); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	})

	// The static page is public (it only contains the login form); everything
	// under /api/ requires the management login. The credentials are checked by
	// this helper, so no DSM session is needed.
	secured := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow the page served by DSM (a different origin, on port 5000) to call
		// this API on port 8321. The preflight must be answered before the auth
		// check, otherwise the browser never sends the real request.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || !auth.check(u, p) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Cloudflare Tunnel", charset="UTF-8"`)
			http.Error(w, "需要登录", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})

	log.Printf("cfdctl listening on %s (version=%s, mode=%s)", *listen, s.version, s.mode())
	if err := http.ListenAndServe(*listen, secured); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
