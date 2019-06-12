package main

import (
	api_module "bitbucket.org/meklis/helpprovider-gopinger/api"
	pinger_module "bitbucket.org/meklis/helpprovider-gopinger/pinger"
	"bitbucket.org/meklis/helpprovider_snmp/logger"
	"flag"
	"fmt"
	"github.com/ztrue/tracerr"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"os"
	"time"
)

var (
	pathConfig string
	Config     Configuration
	lg         *logger.Logger
)

const (
	VERSION = "0.2"
)

func init() {
	flag.StringVar(&pathConfig, "c", "pinger.config.yml", "Path to configuration file  ")
	flag.Parse()
}

type Configuration struct {
	System ConfigurationSystem         `yaml:"system"`
	Api    ConfigurationApi            `yaml:"api"`
	Pinger pinger_module.Configuration `yaml:"pinger"`
}

type ConfigurationSystem struct {
	SleepAfterCheck time.Duration `yaml:"sleep_after_check"`
	PingerIdent     string        `yaml:"pinger_ident"`
	Logger          struct {
		Console struct {
			Enabled      bool `yaml:"enabled"`
			EnabledColor bool `yaml:"enable_color"`
			LogLevel     int  `yaml:"log_level"`
		} `yaml:"console"`
	} `yaml:"logger"`
}
type ConfigurationApi struct {
	HostListAddr   string        `yaml:"host_list_addr"`
	ReportAddr     string        `yaml:"report_addr"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

func main() {
	//Load configuration
	if err := LoadConfig(); err != nil {
		log.Panicln("ERROR LOADING CONFIGURATION FILE: ", err.Error())
		os.Exit(1)
	}
	//Initialize logger
	InitializeLogger()

	api := api_module.NewApi(api_module.Configuration{
		PingerIdent:    Config.System.PingerIdent,
		RequestTimeout: Config.Api.RequestTimeout,
		HostListAddr:   Config.Api.HostListAddr,
		ReportAddr:     Config.Api.ReportAddr,
	})
	err, pinger := pinger_module.NewPinger(Config.Pinger, lg)
	if err != nil {
		log.Panicln("ERROR INITIALIZE PINGER: ", err.Error())
		os.Exit(1)
	}

	for {
		devices, err := api.GetHosts()
		if err != nil {
			lg.Errorf("Error loading hosts list from API. Err: %v", tracerr.Sprint(err))
			time.Sleep(time.Second * 10)
			continue
		}
		responses := pinger.StartPing(devices)
		if len(responses) != 0 {
			err := api.SendUpdate(responses)
			if err != nil {
				lg.Errorf("Error update hosts list. Err: %v", tracerr.Sprint(err))
				time.Sleep(time.Second * 10)
				continue
			}
		}
		time.Sleep(Config.System.SleepAfterCheck)
	}

}

func LoadConfig() error {
	bytes, err := ioutil.ReadFile(pathConfig)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(bytes, &Config)
	if err != nil {
		return err
	}
	return nil
}
func PrintStarted() {
	fmt.Printf(`
Started GOPINGER
ver: %v
`, VERSION)
}
func InitializeLogger() {
	if Config.System.Logger.Console.Enabled {
		color := 0
		if Config.System.Logger.Console.EnabledColor {
			color = 1
		}
		lg, _ = logger.New("pooler", color, os.Stdout)
		lg.SetLogLevel(logger.LogLevel(Config.System.Logger.Console.LogLevel))
		if Config.System.Logger.Console.LogLevel < 5 {
			lg.SetFormat("#%{id} %{time} > %{level} %{message}")
		} else {
			lg.SetFormat("#%{id} %{time} (%{filename}:%{line}) > %{level} %{message}")
		}
	} else {
		lg, _ = logger.New("no_log", 0, os.DevNull)
	}
}
