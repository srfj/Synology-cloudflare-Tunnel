// Command cfdctl is a tiny management helper for the cloudflared Synology
// package. It supervises the cloudflared tunnel process and serves a small
// web UI that shows the tunnel status and the recent log so the service can be
// inspected from DSM without an SSH session.
//
// It only uses the Go standard library so it can be cross-compiled statically
// for old DSM 6.2.4 kernels.
package main

import (
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

// tokenMask is what the UI and the log show in place of the real token. The
// token itself is never sent to the browser.
const tokenMask = "••••••••"

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

// tunnelLaunch returns the arguments and environment used to launch
// cloudflared, or an error when neither a tunnel token nor a config file is
// available.
//
// The token is handed to cloudflared through the TUNNEL_TOKEN_FILE environment
// variable, i.e. a path to the token file rather than the secret itself. This
// keeps the token out of the process list (ps) and out of the process
// environment; cloudflared reads and trims the file itself.
func (s *supervisor) tunnelLaunch() (args, env []string, err error) {
	if b, e := os.ReadFile(s.tokenPath); e == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return []string{"tunnel", "--no-autoupdate", "run"},
				append(os.Environ(), "TUNNEL_TOKEN_FILE="+s.tokenPath), nil
		}
	}
	if fi, e := os.Stat(s.configPath); e == nil && !fi.IsDir() {
		return []string{"tunnel", "--no-autoupdate", "--config", s.configPath, "run"},
			os.Environ(), nil
	}
	return nil, nil, fmt.Errorf("尚未配置：请在套件安装时填入 Tunnel Token")
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
	args, env, err := s.tunnelLaunch()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	cmd := exec.Command(s.binary, args...)
	cmd.Env = env
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
	// TokenMasked is a fixed placeholder shown when a token is configured; the
	// real token is never included in any API response.
	TokenMasked string `json:"token_masked"`
}

func (s *supervisor) status() status {
	pid := s.currentPID()
	mode := s.mode()
	st := status{
		Running:    pid > 0,
		PID:        pid,
		Mode:       mode,
		Configured: mode != "none",
		Version:    s.version,
	}
	if mode == "token" {
		st.TokenMasked = tokenMask
	}
	switch {
	case pid > 0:
		st.Message = "隧道运行中"
	case st.Configured:
		st.Message = "已配置，但当前未运行"
	default:
		st.Message = "未配置：请在套件安装时填入 Tunnel Token"
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

// logTail returns the last lines of the tunnel log with any occurrence of the
// token scrubbed, so the secret can never leak through the log view.
func (s *supervisor) logTail(lines int) string {
	content := tail(s.logPath, lines)
	if b, err := os.ReadFile(s.tokenPath); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			content = strings.ReplaceAll(content, tok, tokenMask)
		}
	}
	return content
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

func main() {
	listen := flag.String("listen", "0.0.0.0:8321", "address to serve the management UI on")
	binary := flag.String("binary", "", "path to the cloudflared binary")
	vardir := flag.String("vardir", "", "writable package data directory")
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

	// Start the tunnel as soon as the helper comes up, so the tunnel runs
	// automatically right after installation and after every NAS reboot.
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
		writeJSON(w, http.StatusOK, map[string]string{"log": s.logTail(n)})
	})
	// Token update is write-only: the request body is stored and the secret is
	// never echoed back. This lets the token be changed without reinstalling.
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

	// No login is required: the package is managed from the DSM LAN address.
	// The page may be served by DSM (a different origin, on port 5000) while
	// the helper listens on 8321, so the API allows cross-origin requests.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})

	log.Printf("cfdctl listening on %s (version=%s, mode=%s)", *listen, s.version, s.mode())
	if err := http.ListenAndServe(*listen, handler); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
