package main

import (
	api_module "bitbucket.org/meklis/helpprovider-gopinger/api"
	pinger_module "bitbucket.org/meklis/helpprovider-gopinger/pinger"
	"bitbucket.org/meklis/helpprovider-gopinger/prom"
	"bitbucket.org/meklis/helpprovider_snmp/logger"
	"flag"
	"fmt"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/ztrue/tracerr"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	pathConfig string
	Config     Configuration
	lg         *logger.Logger
)

const (
	VERSION = "0.2"
	BUILD_DATE = "2020-04-15 19:30"
)

func init() {
	flag.StringVar(&pathConfig, "c", "pinger.config.yml", "Path to configuration file  ")
	flag.Parse()
}

type Configuration struct {
	System ConfigurationSystem         `yaml:"system"`
	Api    ConfigurationApi            `yaml:"api"`
	Pinger pinger_module.Configuration `yaml:"pinger"`
	Prometheus struct {
		Enabled                 bool              `yaml:"enabled"`
		Port                    int               `yaml:"port"`
		Path                    string            `yaml:"path"`
	} `yaml:"prometheus"`
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
	if err := LoadConfig(pathConfig, &Config); err != nil {
		log.Panicln("ERROR LOADING CONFIGURATION FILE: ", err.Error())
		os.Exit(1)
	}
	//Initialize logger
	InitializeLogger()


	//Initialize prometheus
	if Config.Prometheus.Enabled {
		prom.PromEnabled = true
		lg.NoticeF("Exporter for prometheus is enabled...")
		http.Handle(Config.Prometheus.Path, promhttp.Handler())
		go func() {
			err := http.ListenAndServe(fmt.Sprintf(":%v", Config.Prometheus.Port), nil)
			lg.CriticalF("Prometheus exporter critical err: %v", err)
			panic(err)
		}()
		lg.NoticeF("Prometheus exporter started on 0.0.0.0:%v%v", Config.Prometheus.Port, Config.Prometheus.Path)
		prom.SysInfo(VERSION, BUILD_DATE)
	}

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
		start_dur := time.Now().UnixNano()
		responses := pinger.StartPing(devices)
		prom.CicleTimeAdd(float64(time.Now().UnixNano() - start_dur) / float64(time.Second))
		prom.CountCiclesInc()
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

func LoadConfig(path string, Config *Configuration) error {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	yamlConfig := string(bytes)
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		yamlConfig = strings.ReplaceAll(yamlConfig, fmt.Sprintf("${%v}", pair[0]), pair[1])
	}
	err = yaml.Unmarshal([]byte(yamlConfig), &Config)
	fmt.Printf(`Loaded configuration from %v with env readed:
%v
`, path, yamlConfig)
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
