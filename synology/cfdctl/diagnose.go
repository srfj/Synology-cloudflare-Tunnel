package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

type edgeConn struct {
	Index    int    `json:"index"`
	Protocol string `json:"protocol"`
	Location string `json:"location"`
	IP       string `json:"ip"`
}

type diagnose struct {
	InitialProtocol string     `json:"initial_protocol,omitempty"`
	ActiveProtocol  string     `json:"active_protocol,omitempty"`
	Fallback        bool       `json:"fallback"`
	UDPBlocked      bool       `json:"udp_blocked"`
	Hint            string     `json:"hint,omitempty"`
	Connections     []edgeConn `json:"connections,omitempty"`
}

func sessionLog(content string) string {
	lines := strings.Split(content, "\n")
	start := 0
	for i, line := range lines {
		if strings.Contains(line, "Initial protocol ") {
			start = i
		}
	}
	return strings.Join(lines[start:], "\n")
}

func parseDiagnose(content string) diagnose {
	var d diagnose
	conns := map[int]edgeConn{}
	for _, line := range strings.Split(sessionLog(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") {
			parseJSONLogLine(line, &d, conns)
			continue
		}
		parseConsoleLogLine(line, &d, conns)
	}
	if len(conns) > 0 {
		keys := make([]int, 0, len(conns))
		for k := range conns {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		d.Connections = make([]edgeConn, 0, len(keys))
		for _, k := range keys {
			d.Connections = append(d.Connections, conns[k])
		}
		last := d.Connections[len(d.Connections)-1]
		if last.Protocol != "" {
			d.ActiveProtocol = last.Protocol
		}
	}
	d.Hint = diagnoseHint(d)
	return d
}

func parseJSONLogLine(line string, d *diagnose, conns map[int]edgeConn) {
	var evt map[string]any
	if err := json.Unmarshal([]byte(line), &evt); err != nil {
		parseConsoleLogLine(line, d, conns)
		return
	}
	msg := jsonString(evt["message"])
	applyLogMessage(msg, d)
	if strings.Contains(msg, "Registered tunnel connection") {
		idx := jsonInt(evt["connIndex"])
		conns[idx] = edgeConn{
			Index:    idx,
			Protocol: jsonString(evt["protocol"]),
			Location: jsonString(evt["location"]),
			IP:       jsonString(evt["ip"]),
		}
		if conns[idx].Protocol != "" {
			d.ActiveProtocol = conns[idx].Protocol
		}
	}
}

func parseConsoleLogLine(line string, d *diagnose, conns map[int]edgeConn) {
	applyLogMessage(line, d)
	if !strings.Contains(line, "Registered tunnel connection") {
		return
	}
	kv := parseLogFields(line)
	idx, _ := strconv.Atoi(kv["connIndex"])
	c := edgeConn{
		Index:    idx,
		Protocol: kv["protocol"],
		Location: kv["location"],
		IP:       kv["ip"],
	}
	conns[idx] = c
	if c.Protocol != "" {
		d.ActiveProtocol = c.Protocol
	}
}

func applyLogMessage(msg string, d *diagnose) {
	if i := strings.Index(msg, "Initial protocol "); i >= 0 {
		rest := strings.TrimSpace(msg[i+len("Initial protocol "):])
		if fields := strings.Fields(rest); len(fields) > 0 {
			d.InitialProtocol = strings.Trim(fields[0], `",`)
			if d.ActiveProtocol == "" {
				d.ActiveProtocol = d.InitialProtocol
			}
		}
	}
	if i := strings.Index(msg, "Switching to fallback protocol "); i >= 0 {
		d.Fallback = true
		rest := strings.TrimSpace(msg[i+len("Switching to fallback protocol "):])
		if fields := strings.Fields(rest); len(fields) > 0 {
			d.ActiveProtocol = strings.Trim(fields[0], `",`)
		}
	}
	if strings.Contains(msg, "UDP to port 7844") {
		d.UDPBlocked = true
	}
}

func parseLogFields(line string) map[string]string {
	out := map[string]string{}
	for _, f := range strings.Fields(line) {
		k, v, ok := strings.Cut(f, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func jsonString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func jsonInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	default:
		return 0
	}
}

func diagnoseHint(d diagnose) string {
	switch {
	case d.UDPBlocked:
		return "家宽可能拦截了到 Cloudflare 的 UDP 7844，QUIC 建连失败。请改用「稳定优先（HTTP/2）」，或在路由器放行 UDP 7844 出站。"
	case d.Fallback:
		return "已从 QUIC 回退到 HTTP/2，网页可能变慢。可先用「稳定优先」减少重试，或排查 UDP 出站。"
	case d.ActiveProtocol == "http2":
		return "当前走 HTTP/2（TCP）。若运营商未拦 UDP，可试「低延迟（QUIC）」；更慢或断线再切回。"
	case d.ActiveProtocol == "quic":
		return "当前走 QUIC。页面元素仍慢时，可试强制 IPv4，或勾选禁用 QUIC PMTU 探测。"
	default:
		return ""
	}
}
