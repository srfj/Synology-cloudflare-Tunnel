package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type accelSettings struct {
	Preset          string `json:"preset"`
	Protocol        string `json:"protocol"`
	EdgeIPVersion   string `json:"edge_ip_version"`
	HAConnections   int    `json:"ha_connections"`
	DisableQUICPMTU bool   `json:"disable_quic_pmtu"`
}

func defaultAccel() accelSettings {
	return accelSettings{
		Preset:        "auto",
		Protocol:      "auto",
		EdgeIPVersion: "auto",
		HAConnections: 4,
	}
}

func (s *accelSettings) applyPreset() {
	switch s.Preset {
	case "quic":
		s.Protocol = "quic"
	case "http2":
		s.Protocol = "http2"
	default:
		s.Preset = "auto"
		s.Protocol = "auto"
	}
}

func (s *accelSettings) normalize() {
	if s.Preset == "" {
		s.Preset = "auto"
	}
	s.applyPreset()
	if s.EdgeIPVersion == "" {
		s.EdgeIPVersion = "auto"
	}
	if s.HAConnections == 0 {
		s.HAConnections = 4
	}
}

func (s accelSettings) validate() error {
	switch s.Preset {
	case "auto", "quic", "http2":
	default:
		return fmt.Errorf("未知预设: %s", s.Preset)
	}
	switch s.EdgeIPVersion {
	case "auto", "4", "6":
	default:
		return fmt.Errorf("边缘 IP 版本必须是 auto、4 或 6")
	}
	if s.HAConnections < 1 || s.HAConnections > 8 {
		return fmt.Errorf("HA 连接数必须在 1 到 8 之间")
	}
	return nil
}

func (s accelSettings) flags() []string {
	out := []string{
		"--protocol", s.Protocol,
		"--edge-ip-version", s.EdgeIPVersion,
		"--ha-connections", strconv.Itoa(s.HAConnections),
	}
	if s.DisableQUICPMTU {
		out = append(out, "--quic-disable-pmtu-discovery")
	}
	return out
}

func loadAccel(path string) accelSettings {
	s := defaultAccel()
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return defaultAccel()
	}
	s.normalize()
	if err := s.validate(); err != nil {
		return defaultAccel()
	}
	return s
}

func saveAccel(path string, s accelSettings) error {
	s.normalize()
	if err := s.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
