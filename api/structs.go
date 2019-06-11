package api

import "time"

type Configuration struct {
	HostListAddr   string        `yaml:"host_list_addr"`
	ReportAddr     string        `yaml:"report_addr"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

type API struct {
	Config Configuration
}
