package main

import (
	"time"
	"bitbucket.org/meklis/helpprovider-gopinger/pinger"
)

type Configuration struct {
	System ConfigurationSystem `yaml:"system"`
	Api ConfigurationApi `yaml:"api"`
	Pinger pinger.Configuration `yaml:"pinger"`
}

type ConfigurationSystem struct {
	SleepAfterCheck time.Duration `yaml:"sleep_after_check"`
	PingerIdent string `yaml:"pinger_ident"`
	Logger struct {
		Console struct  {
			Enabled bool  `yaml:"enabled"`
			EnabledColor bool `yaml:"enable_color"`
			LogLevel int `yaml:"log_level"`
		} `yaml:"console"`
	} `yaml:"logger"`
}
type ConfigurationApi struct {
	HostListAddr string `yaml:"host_list_addr"`
	ReportAddr string `yaml:"report_addr"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
}