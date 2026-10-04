// Command cfdctl is a tiny management helper for the cloudflared Synology
// package. It supervises the cloudflared tunnel process and serves a small
// web UI so the tunnel can be configured (paste a token), started, stopped
// and inspected from DSM without an SSH session.
//
// It only uses the Go standard library so it can be cross-compiled statically
// for old DSM 6.2.4 kernels.
package main

import (
	"crypto/tls"
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

// dsmAuth validates a DSM session id against DSM's own web API, so the
// management UI can only be used while the user is logged into DSM.
//
// The browser sends DSM's session cookie to this helper as well (cookies are
// not scoped by port), which lets us ask DSM whether that session is still
// logged in. Requests without a valid session are rejected with HTTP 401.
type dsmAuth struct {
	bases  []string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]authCacheEntry
}

type authCacheEntry struct {
	ok  bool
	exp time.Time
}

// dsmAuthProbes are login-required DSM web APIs, tried in order. A logged-in
// session makes at least one of them succeed; error code 119 means the session
// is invalid. Unknown-API codes (102/103) are skipped, so the list works
// across DSM versions.
var dsmAuthProbes = []string{
	"api=SYNO.Core.CurrentUser&version=1&method=get",
	"api=SYNO.FileStation.Info&version=2&method=get",
	"api=SYNO.Core.Desktop.Initdata&version=1&method=get",
}

func newDSMAuth(extra string) *dsmAuth {
	bases := make([]string, 0, 3)
	if extra != "" {
		bases = append(bases, strings.TrimRight(extra, "/"))
	}
	// DSM listens on 5000 (http) and 5001 (https) by default; try both so the
	// check works whether the NAS is reachable over HTTP or HTTPS.
	bases = append(bases, "http://127.0.0.1:5000", "https://127.0.0.1:5001")
	return &dsmAuth{
		bases: bases,
		client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
		cache: make(map[string]authCacheEntry),
	}
}

func (a *dsmAuth) cached(sid string) (bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e, ok := a.cache[sid]; ok && time.Now().Before(e.exp) {
		return e.ok, true
	}
	return false, false
}

func (a *dsmAuth) store(sid string, ok bool) {
	ttl := 30 * time.Second
	if !ok {
		// Short negative cache: a fresh login gets a new session id anyway,
		// but this keeps a logged-out client from hammering DSM.
		ttl = 5 * time.Second
	}
	a.mu.Lock()
	a.cache[sid] = authCacheEntry{ok: ok, exp: time.Now().Add(ttl)}
	a.mu.Unlock()
}

// validate reports whether sid belongs to a logged-in DSM user. It returns an
// error only when DSM itself cannot be reached, so callers can distinguish
// "not logged in" (401) from "cannot verify" (503).
func (a *dsmAuth) validate(sid string) (bool, error) {
	if sid == "" {
		return false, nil
	}
	if ok, hit := a.cached(sid); hit {
		return ok, nil
	}

	reached := false
	for _, base := range a.bases {
		for _, probe := range dsmAuthProbes {
			ok, known, err := a.probe(base, probe, sid)
			if err != nil {
				break // this base URL is unreachable; try the next one
			}
			reached = true
			if !known {
				continue // API not present on this DSM; try the next probe
			}
			if ok {
				a.store(sid, true)
				return true, nil
			}
		}
		if reached {
			break // DSM answered; the session is simply not logged in
		}
	}
	if !reached {
		return false, fmt.Errorf("DSM 未响应")
	}
	a.store(sid, false)
	return false, nil
}

// probe calls a single DSM API. known is false when the API does not exist on
// this DSM build (so the caller should try another probe).
func (a *dsmAuth) probe(base, probe, sid string) (ok, known bool, err error) {
	req, err := http.NewRequest(http.MethodGet, base+"/webapi/entry.cgi?"+probe, nil)
	if err != nil {
		return false, false, err
	}
	req.Header.Set("Cookie", "id="+sid)
	resp, err := a.client.Do(req)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Success bool `json:"success"`
		Error   *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&out); err != nil {
		return false, false, err
	}
	if out.Success {
		return true, true, nil
	}
	if out.Error != nil && (out.Error.Code == 102 || out.Error.Code == 103) {
		return false, false, nil // API not found -> not conclusive
	}
	return false, true, nil // API exists but rejected the session
}

// sessionID extracts DSM's session id ("id" cookie) from the request.
func sessionID(r *http.Request) string {
	if c, err := r.Cookie("id"); err == nil {
		return c.Value
	}
	return ""
}

func main() {
	listen := flag.String("listen", "0.0.0.0:8321", "address to serve the management UI on")
	binary := flag.String("binary", "", "path to the cloudflared binary")
	vardir := flag.String("vardir", "", "writable package data directory")
	dsmurl := flag.String("dsmurl", "", "DSM base URL used to verify the login session (default: auto, tries http://127.0.0.1:5000 and https://127.0.0.1:5001)")
	noauth := flag.Bool("noauth", false, "disable DSM login check (NOT recommended)")
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

	// Gate every API call on a valid DSM login session. The page is served by
	// DSM from /webman/3rdparty/cloudflared/, so the browser sends DSM's "id"
	// cookie here too; we ask DSM to confirm it before doing anything.
	auth := newDSMAuth(*dsmurl)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS: echo the caller's origin (never "*") so credentialed requests
		// are allowed when the page is served from the DSM origin.
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") && !*noauth {
			ok, err := auth.validate(sessionID(r))
			switch {
			case err != nil:
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "无法连接 DSM 校验登录状态: " + err.Error(),
				})
				return
			case !ok:
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "未登录 DSM 或登录已过期，请先登录 DSM 后再打开",
				})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})

	log.Printf("cfdctl listening on %s (version=%s, mode=%s, auth=%v)", *listen, s.version, s.mode(), !*noauth)
	if err := http.ListenAndServe(*listen, handler); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
