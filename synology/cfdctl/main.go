// Command cfdctl is a tiny management helper for the cloudflared Synology
// package. It supervises the cloudflared tunnel process and serves a small
// web UI so the tunnel can be configured (paste a token), started, stopped
// and inspected from DSM without an SSH session.
//
// It only uses the Go standard library so it can be cross-compiled statically
// for old DSM 6.2.4 kernels.
package main

import (
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

// configured returns the arguments used to launch cloudflared, or an error
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
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

	log.Printf("cfdctl listening on %s (version=%s, mode=%s)", *listen, s.version, s.mode())
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cloudflare Tunnel</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { margin: 0; font-family: -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
         background: #f5f6f8; color: #1f2328; }
  .wrap { max-width: 720px; margin: 0 auto; padding: 24px 16px 48px; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .sub { color: #6b7280; font-size: 13px; margin-bottom: 20px; }
  .card { background: #fff; border: 1px solid #e5e7eb; border-radius: 12px; padding: 18px; margin-bottom: 16px; }
  .row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .dot { width: 10px; height: 10px; border-radius: 50%; background: #9ca3af; flex: none; }
  .dot.on { background: #16a34a; box-shadow: 0 0 0 4px rgba(22,163,74,.15); }
  .dot.off { background: #dc2626; box-shadow: 0 0 0 4px rgba(220,38,38,.12); }
  .state { font-weight: 600; }
  .meta { margin-top: 10px; color: #6b7280; font-size: 13px; line-height: 1.7; }
  textarea { width: 100%; min-height: 88px; padding: 10px; border: 1px solid #d1d5db; border-radius: 8px;
             font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 12px; resize: vertical; }
  button { border: 0; border-radius: 8px; padding: 9px 16px; font-size: 14px; cursor: pointer; }
  .primary { background: #f6821f; color: #fff; }
  .ghost { background: #eef0f3; color: #1f2328; }
  button:disabled { opacity: .5; cursor: not-allowed; }
  pre { background: #0b1020; color: #d7e0ff; padding: 12px; border-radius: 8px; overflow: auto;
        max-height: 320px; font-size: 12px; line-height: 1.55; margin: 0; }
  .toast { margin-top: 10px; font-size: 13px; min-height: 18px; }
  .ok { color: #16a34a; } .err { color: #dc2626; }
</style>
</head>
<body>
<div class="wrap">
  <h1>Cloudflare Tunnel</h1>
  <div class="sub" id="version">cloudflared</div>

  <div class="card">
    <div class="row">
      <span class="dot" id="dot"></span>
      <span class="state" id="state">读取中…</span>
      <span style="flex:1"></span>
      <button class="ghost" onclick="act('start')">启动</button>
      <button class="ghost" onclick="act('stop')">停止</button>
      <button class="ghost" onclick="act('restart')">重启</button>
    </div>
    <div class="meta" id="meta"></div>
  </div>

  <div class="card">
    <div class="row" style="margin-bottom:10px"><strong>Tunnel Token</strong></div>
    <textarea id="token" placeholder="粘贴 Cloudflare Zero Trust 里创建的隧道 Token"></textarea>
    <div class="row" style="margin-top:10px">
      <button class="primary" onclick="saveToken()">保存并启动</button>
      <span class="toast" id="toast"></span>
    </div>
    <div class="meta">Token 只保存在本机：/var/packages/cloudflared/var/tunnel-token</div>
  </div>

  <div class="card">
    <div class="row" style="margin-bottom:10px"><strong>运行日志</strong><span style="flex:1"></span>
      <button class="ghost" onclick="loadLog()">刷新</button></div>
    <pre id="log">加载中…</pre>
  </div>
</div>
<script>
async function api(path, opts) {
  const r = await fetch(path, opts);
  const t = await r.text();
  try { return JSON.parse(t); } catch (e) { return { error: t }; }
}
function toast(msg, ok) {
  const el = document.getElementById('toast');
  el.textContent = msg; el.className = 'toast ' + (ok ? 'ok' : 'err');
  setTimeout(() => { el.textContent = ''; }, 4000);
}
async function refresh() {
  const s = await api('/api/status');
  const dot = document.getElementById('dot');
  dot.className = 'dot ' + (s.running ? 'on' : 'off');
  document.getElementById('state').textContent = s.message || '';
  document.getElementById('meta').textContent =
    '模式: ' + s.mode + '   PID: ' + (s.pid || '-') + '   ' + (s.version || '');
}
async function act(name) {
  const r = await api('/api/' + name, { method: 'POST' });
  toast(r.ok ? '操作成功' : (r.error || '操作失败'), r.ok);
  refresh();
}
async function saveToken() {
  const v = document.getElementById('token').value.trim();
  if (!v) { toast('Token 不能为空', false); return; }
  const r = await api('/api/token', { method: 'POST', body: v });
  toast(r.ok ? '已保存并启动' : (r.error || '失败'), r.ok);
  refresh();
}
async function loadLog() {
  const r = await api('/api/log?lines=200');
  const el = document.getElementById('log');
  el.textContent = r.log || '(暂无日志)';
  el.scrollTop = el.scrollHeight;
}
refresh(); loadLog();
setInterval(refresh, 5000);
setInterval(loadLog, 5000);
</script>
</body>
</html>
`
