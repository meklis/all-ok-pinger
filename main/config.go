package main

import (
	"bitbucket.org/meklis/helpprovider-gopinger/pinger"
	"time"
)

type Configuration struct {
	Ident             string               `yaml:"name"`
	TimeoutAfterCheck time.Duration        `yaml:"timeout_after_check"`
	PingerConfig      pinger.Configuration `yaml:"pinger"`
}
