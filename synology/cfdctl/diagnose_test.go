package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseConsoleDiagnose(t *testing.T) {
	t.Parallel()
	log := `
2026-10-11T01:00:00Z INF Initial protocol quic
2026-10-11T01:00:01Z INF Registered tunnel connection connIndex=0 connection=abc location=HKG ip=198.18.0.1 protocol=quic
2026-10-11T01:00:02Z INF Registered tunnel connection connIndex=1 connection=def location=HKG ip=198.18.0.2 protocol=quic
`
	d := parseDiagnose(log)
	assert.Equal(t, "quic", d.InitialProtocol)
	assert.Equal(t, "quic", d.ActiveProtocol)
	assert.False(t, d.Fallback)
	assert.Len(t, d.Connections, 2)
	assert.Equal(t, "HKG", d.Connections[0].Location)
	assert.Contains(t, d.Hint, "QUIC")
}

func TestParseFallbackAndUDP(t *testing.T) {
	t.Parallel()
	log := `
2026-10-11T01:00:00Z INF Initial protocol quic
2026-10-11T01:00:01Z WRN If this log occurs persistently, UDP to port 7844 (or others) blocked or dropped.
2026-10-11T01:00:02Z INF Switching to fallback protocol http2
2026-10-11T01:00:03Z INF Registered tunnel connection connIndex=0 location=NRT ip=1.1.1.1 protocol=http2
`
	d := parseDiagnose(log)
	assert.True(t, d.UDPBlocked)
	assert.True(t, d.Fallback)
	assert.Equal(t, "http2", d.ActiveProtocol)
	assert.Contains(t, d.Hint, "UDP 7844")
}

func TestParseJSONDiagnose(t *testing.T) {
	t.Parallel()
	log := `{"level":"info","message":"Initial protocol auto"}
{"level":"info","connIndex":0,"location":"SIN","ip":"1.0.0.1","protocol":"quic","message":"Registered tunnel connection"}
`
	d := parseDiagnose(log)
	assert.Equal(t, "auto", d.InitialProtocol)
	assert.Equal(t, "quic", d.ActiveProtocol)
	assert.Equal(t, []edgeConn{{Index: 0, Protocol: "quic", Location: "SIN", IP: "1.0.0.1"}}, d.Connections)
}

func TestParseUsesLastSession(t *testing.T) {
	t.Parallel()
	log := `
INF Initial protocol quic
WRN UDP to port 7844 blocked
INF Switching to fallback protocol http2
INF Initial protocol http2
INF Registered tunnel connection connIndex=0 protocol=http2 location=LAX ip=8.8.8.8
`
	d := parseDiagnose(log)
	assert.False(t, d.UDPBlocked)
	assert.False(t, d.Fallback)
	assert.Equal(t, "http2", d.InitialProtocol)
	assert.Equal(t, "http2", d.ActiveProtocol)
}

func TestParseHTTP2Hint(t *testing.T) {
	t.Parallel()
	d := parseDiagnose("INF Initial protocol http2\nINF Registered tunnel connection connIndex=0 protocol=http2 location=LAX ip=8.8.8.8")
	assert.Equal(t, "http2", d.ActiveProtocol)
	assert.Contains(t, d.Hint, "HTTP/2")
}
