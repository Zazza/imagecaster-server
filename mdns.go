package main

import (
	"github.com/grandcat/zeroconf"
)

type MDNSService struct {
	server  *zeroconf.Server
	port    int
	enabled bool
	version string
}

func NewMDNS(port int, enabled bool, version string) *MDNSService {
	return &MDNSService{port: port, enabled: enabled, version: version}
}

func (m *MDNSService) Register() error {
	if !m.enabled {
		return nil
	}

	server, err := zeroconf.Register(
		"Imagecaster Server",
		"_imagecaster._tcp",
		"local.",
		m.port,
		[]string{"version=" + m.version},
		nil,
	)
	if err != nil {
		return err
	}
	m.server = server
	return nil
}

func (m *MDNSService) Shutdown() {
	if m.server != nil {
		m.server.Shutdown()
	}
}
