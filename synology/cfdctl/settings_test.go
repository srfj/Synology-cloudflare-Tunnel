package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultAccelFlags(t *testing.T) {
	t.Parallel()
	s := defaultAccel()
	assert.Equal(t, []string{
		"--protocol", "auto",
		"--edge-ip-version", "auto",
		"--ha-connections", "4",
	}, s.flags())
}

func TestApplyPreset(t *testing.T) {
	t.Parallel()
	cases := []struct {
		preset   string
		protocol string
	}{
		{"auto", "auto"},
		{"quic", "quic"},
		{"http2", "http2"},
		{"", "auto"},
	}
	for _, tc := range cases {
		s := accelSettings{Preset: tc.preset}
		s.normalize()
		assert.Equal(t, tc.protocol, s.Protocol, tc.preset)
		require.NoError(t, s.validate())
	}
}

func TestValidateHAConnections(t *testing.T) {
	t.Parallel()
	s := defaultAccel()
	s.HAConnections = 0
	s.normalize()
	assert.Equal(t, 4, s.HAConnections)

	s.HAConnections = 9
	assert.Error(t, s.validate())
	s.HAConnections = 1
	assert.NoError(t, s.validate())
	s.EdgeIPVersion = "7"
	assert.Error(t, s.validate())
}

func TestFlagsIncludePMTU(t *testing.T) {
	t.Parallel()
	s := defaultAccel()
	s.Preset = "quic"
	s.DisableQUICPMTU = true
	s.normalize()
	assert.Equal(t, []string{
		"--protocol", "quic",
		"--edge-ip-version", "auto",
		"--ha-connections", "4",
		"--quic-disable-pmtu-discovery",
	}, s.flags())
}

func TestTunnelLaunchIncludesAccelFlags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	token := filepath.Join(dir, "tunnel-token")
	accel := filepath.Join(dir, "accel.json")
	require.NoError(t, os.WriteFile(token, []byte("tok\n"), 0o600))
	require.NoError(t, saveAccel(accel, accelSettings{Preset: "http2", EdgeIPVersion: "4", HAConnections: 2, DisableQUICPMTU: true}))
	s := &supervisor{tokenPath: token, configPath: filepath.Join(dir, "missing.yml"), accelPath: accel}
	args, env, err := s.tunnelLaunch()
	require.NoError(t, err)
	assert.Equal(t, []string{
		"tunnel", "--no-autoupdate",
		"--protocol", "http2",
		"--edge-ip-version", "4",
		"--ha-connections", "2",
		"--quic-disable-pmtu-discovery",
		"run",
	}, args)
	found := false
	for _, e := range env {
		if e == "TUNNEL_TOKEN_FILE="+token {
			found = true
		}
		assert.NotEqual(t, "TUNNEL_TOKEN=tok", e)
		assert.NotContains(t, e, "TUNNEL_TOKEN=tok")
	}
	assert.True(t, found)
}

func TestLoadSaveAccel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "accel.json")
	assert.Equal(t, defaultAccel(), loadAccel(path))

	in := accelSettings{Preset: "http2", EdgeIPVersion: "4", HAConnections: 2}
	require.NoError(t, saveAccel(path, in))
	got := loadAccel(path)
	assert.Equal(t, "http2", got.Preset)
	assert.Equal(t, "http2", got.Protocol)
	assert.Equal(t, "4", got.EdgeIPVersion)
	assert.Equal(t, 2, got.HAConnections)

	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))
	assert.Equal(t, defaultAccel(), loadAccel(path))
}
