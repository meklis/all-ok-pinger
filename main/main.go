package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	api_module "github.com/meklis/all-ok-pinger/api"
	"github.com/meklis/all-ok-pinger/events"
	pinger_module "github.com/meklis/all-ok-pinger/pinger"
	"github.com/meklis/all-ok-pinger/prom"
	"github.com/meklis/all-ok-pinger/state"
	"github.com/meklis/all-ok-pinger/web"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/ztrue/tracerr"
	"gopkg.in/yaml.v2"
)

var (
	pathConfig string
	Config     Configuration
	lg         *logrus.Logger
)

// set on build: -ldflags "-X main.VERSION=... -X main.BUILD_DATE=..."
var (
	VERSION    = "1.0.0"
	BUILD_DATE = "2026-11-11"
)

func init() {
	flag.StringVar(&pathConfig, "c", "pinger.config.yml", "Path to configuration file  ")
	flag.Parse()
}

type Configuration struct {
	// Ident - identifier of pinger, sent to API in ?ident=
	Ident string `yaml:"ident"`
	// SleepInterval - fixed period of cycles. If cycle takes longer - next cycle starts immediately. 0 - without pause
	SleepInterval time.Duration `yaml:"sleep_interval"`
	Log           struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"log"`
	Api      ConfigurationApi         `yaml:"api"`
	ICMP     pinger_module.ICMPConfig `yaml:"icmp"`
	TCP      pinger_module.TCPConfig  `yaml:"tcp"`
	Realtime state.RealtimeConfig     `yaml:"realtime"`
	Web      struct {
		Listen string `yaml:"listen"`
		Pprof  bool   `yaml:"pprof"`
	} `yaml:"web"`
	Prometheus struct {
		Enabled         bool   `yaml:"enabled"`
		HostMetrics     string `yaml:"host_metrics"`
		LatencyMetrics  string `yaml:"latency_metrics"`
		AttributeLabels string `yaml:"attribute_labels"`
	} `yaml:"prometheus"`
	Redis events.Config `yaml:"redis"`
}

type ConfigurationApi struct {
	HostListUrl string `yaml:"host_list_url"`
	// HostListRefreshInterval - host list is loaded in background with this interval
	HostListRefreshInterval time.Duration `yaml:"host_list_refresh_interval"`
	// IgnoreHostListStatuses - statuses from host list are ignored (as status: null)
	IgnoreHostListStatuses bool          `yaml:"ignore_host_list_statuses"`
	ReportUrl              string        `yaml:"report_url"`
	ReportEnabled          bool          `yaml:"report_enabled"`
	Timeout                time.Duration `yaml:"timeout"`
}

func (c *Configuration) prepare() error {
	if c.Ident == "" {
		c.Ident = "PINGER"
	}
	if c.Api.HostListUrl == "" {
		return fmt.Errorf("api.host_list_url is required")
	}
	if c.Api.HostListRefreshInterval <= 0 {
		c.Api.HostListRefreshInterval = 5 * time.Minute
	}
	if c.Api.Timeout <= 0 {
		c.Api.Timeout = 30 * time.Second
	}
	if c.Api.ReportUrl == "" {
		c.Api.ReportEnabled = false
	}
	if c.Web.Listen == "" {
		c.Web.Listen = ":2112"
	}
	return nil
}

func main() {
	if err := LoadConfig(pathConfig, &Config); err != nil {
		log.Fatalln("Error loading configuration: ", err.Error())
	}
	if err := Config.prepare(); err != nil {
		log.Fatalln("Error in configuration: ", err.Error())
	}
	InitializeLogger()
	lg.Infof("Started pinger %v, ident=%v", VERSION, Config.Ident)

	pinger, err := pinger_module.NewPinger(pinger_module.Configuration{ICMP: Config.ICMP, TCP: Config.TCP}, lg)
	if err != nil {
		lg.Fatalf("Error initialize pinger: %v", err)
	}
	// defaults are filled by pinger
	Config.ICMP, Config.TCP = pinger.Config.ICMP, pinger.Config.TCP

	store := state.NewStore()
	store.SetRealtimeConfig(Config.Realtime)
	reportEnabled := Config.Api.ReportEnabled
	store.SetReporting(reportEnabled, Config.Api.IgnoreHostListStatuses)
	srv := startHTTP(store)
	store.OnEvent(srv.Event)

	var publisher *events.Publisher
	if Config.Redis.Enabled {
		publisher, err = events.New(Config.Redis, Config.Ident, lg)
		if err != nil {
			lg.Fatalf("Error in redis configuration: %v", err)
		}
		store.OnEvent(publisher.Event)
	}

	api := api_module.NewApi(api_module.Configuration{
		PingerIdent:    Config.Ident,
		RequestTimeout: Config.Api.Timeout,
		HostListAddr:   Config.Api.HostListUrl,
		ReportAddr:     Config.Api.ReportUrl,
		Logger:         lg,
	})
	pinger.OnInspection = func(inspection int, states []pinger_module.InspectionState) {
		store.UpdateInspection(inspection, states)
		srv.Inspection(store.Progress(), store.PendingList())
	}
	if reportEnabled {
		lg.Infof("Reports enabled, only changed statuses are reported")
	} else {
		lg.Infof("Reports are disabled")
	}
	if Config.Api.IgnoreHostListStatuses {
		lg.Infof("Statuses from host list are ignored, statuses of previous pass are used")
	}

	// Host list: first load is blocking, then refresh in background
	for !loadHostList(api, store) {
		time.Sleep(time.Second * 10)
	}
	go func() {
		interval := Config.Api.HostListRefreshInterval
		lg.Infof("Host list refresh interval: %v", interval)
		for {
			time.Sleep(interval)
			for !loadHostList(api, store) {
				time.Sleep(time.Second * 30)
			}
		}
	}()

	lg.Infof("Cycle period (sleep_interval): %v", Config.SleepInterval)
	for {
		cycleStart := time.Now()
		devices := store.Devices()
		store.StartCycle(len(devices), Config.ICMP.Inspections)

		results := pinger.StartPing(devices)
		duration := time.Since(cycleStart)
		prom.CicleTimeAdd(duration.Seconds())
		prom.CountCiclesInc()
		store.Update(results)

		info := state.CycleInfo{
			StartedAt:   cycleStart,
			FinishedAt:  time.Now(),
			DurationSec: duration.Seconds(),
			Hosts:       len(devices),
		}
		changed := make([]string, 0)
		for _, r := range results {
			if r.Changed && r.Error == "" {
				changed = append(changed, r.Device.Ip)
				if r.Device.Status != nil && r.Status != nil && (*r.Device.Status > 0) != (*r.Status > 0) {
					info.Flaps++
				}
			}
		}
		info.Changed = len(changed)
		lg.Infof("Cycle finished in %v, hosts=%v, changed=%v, up<->down=%v", duration.Round(time.Millisecond), len(devices), len(changed), info.Flaps)

		// redis: real changes of cycle
		if publisher != nil && len(changed) > 0 {
			objects := make([]map[string]interface{}, 0, len(changed))
			for _, ip := range changed {
				if h, ok := store.Get(ip); ok {
					objects = append(objects, h.ReportObject())
				}
			}
			publisher.Report(objects)
		}
		// report: hosts with status, which differs from status known by server
		// (changed in this cycle, not sent because of error, or server has another status in host list)
		if reportEnabled {
			pending := store.ReportPending()
			prom.ReportPending(len(pending))
			if len(pending) > 0 {
				objects := make([]map[string]interface{}, 0, len(pending))
				for _, ip := range pending {
					if h, ok := store.Get(ip); ok {
						objects = append(objects, h.ReportObject())
					}
				}
				err := api.SendUpdate(objects)
				prom.ReportSent(len(objects), err)
				if err != nil {
					lg.Errorf("Error sending report of %v hosts, will be retried in next cycle. Err: %v", len(objects), tracerr.Sprint(err))
					info.ReportError = err.Error()
				} else {
					lg.Infof("Report sent: %v hosts (changed in cycle: %v)", len(objects), len(changed))
					info.ReportSent = len(objects)
					store.MarkReported(pending, time.Now())
					prom.ReportPending(len(store.ReportPending()))
				}
			}
		}
		store.SetCycleInfo(info)
		prom.CycleFinished(info.FinishedAt)
		srv.Cycle(info)
		// fixed period: failed report will be retried in the next cycle
		if period := Config.SleepInterval; period > 0 {
			if elapsed := time.Since(cycleStart); elapsed < period {
				time.Sleep(period - elapsed)
			} else {
				lg.Warnf("Cycle took %v, longer than sleep_interval %v, starting next cycle immediately", elapsed.Round(time.Millisecond), period)
			}
		}
	}
}

// loadHostList - load host list from API and sync with store
func loadHostList(api *api_module.API, store *state.Store) bool {
	start := time.Now()
	devices, err := api.GetHosts()
	if err != nil {
		lg.Errorf("Error loading hosts list from API. Err: %v", tracerr.Sprint(err))
		prom.HostListErrorInc()
		store.SetHostListError(err)
		return false
	}
	info := store.SyncHosts(devices)
	prom.HostListLoaded(info.Hosts, time.Since(start).Seconds(), time.Now())
	lg.Infof("Host list loaded in %v: hosts=%v, added=%v, updated=%v, removed=%v",
		time.Since(start).Round(time.Millisecond), info.Hosts, info.Added, info.Updated, info.Removed)
	return true
}

// startHTTP - web server: dashboard, API and prometheus metrics (/metrics)
func startHTTP(store *state.Store) *web.Server {
	srv := web.NewServer(store, web.Info{
		Version:     VERSION,
		BuildDate:   BUILD_DATE,
		PingerIdent: Config.Ident,
	}, publicConfig())
	if Config.Web.Pprof {
		srv.EnablePprof()
	}
	if Config.Prometheus.Enabled {
		prom.PromEnabled = true
		collector, err := state.NewCollector(store, Config.Prometheus.HostMetrics, Config.Prometheus.LatencyMetrics, Config.Prometheus.AttributeLabels)
		if err != nil {
			lg.Fatalf("Error in prometheus configuration: %v", err)
		}
		prometheus.MustRegister(collector)
		prom.SysInfo(VERSION, BUILD_DATE)
		srv.Handle("GET /metrics", promhttp.Handler())
	}
	go func() {
		lg.Fatalf("Web server error: %v", http.ListenAndServe(Config.Web.Listen, srv.Handler()))
	}()
	lg.Infof("Web server started on %v (prometheus metrics: %v)", Config.Web.Listen, Config.Prometheus.Enabled)
	return srv
}

// publicConfig - configuration for web UI, without credentials
func publicConfig() map[string]interface{} {
	return map[string]interface{}{
		"ident":          Config.Ident,
		"sleep_interval": Config.SleepInterval.String(),
		"log":            map[string]interface{}{"level": lg.GetLevel().String(), "format": Config.Log.Format},
		"api": map[string]interface{}{
			"host_list_url":              maskURL(Config.Api.HostListUrl),
			"host_list_refresh_interval": Config.Api.HostListRefreshInterval.String(),
			"ignore_host_list_statuses":  Config.Api.IgnoreHostListStatuses,
			"report_url":                 maskURL(Config.Api.ReportUrl),
			"report_enabled":             Config.Api.ReportEnabled,
			"timeout":                    Config.Api.Timeout.String(),
		},
		"icmp": map[string]interface{}{
			"inspections":         Config.ICMP.Inspections,
			"success_for_up":      Config.ICMP.SuccessForUp,
			"failed_for_down":     Config.ICMP.FailedForDown,
			"packets":             Config.ICMP.Packets,
			"packets_per_second":  Config.ICMP.PacketsPerSecond,
			"response_timeout":    Config.ICMP.ResponseTimeout.String(),
			"inspection_interval": Config.ICMP.InspectionInterval.String(),
			"ipv6":                Config.ICMP.IPv6,
		},
		"tcp": map[string]interface{}{
			"enabled": Config.TCP.Enabled,
			"ports":   Config.TCP.Ports,
			"workers": Config.TCP.Workers,
			"timeout": Config.TCP.Timeout.String(),
		},
		"realtime": map[string]interface{}{
			"falling_after_inspections": Config.Realtime.FallingAfterInspections,
			"rising_after_inspections":  Config.Realtime.RisingAfterInspections,
		},
		"web": map[string]interface{}{"listen": Config.Web.Listen, "pprof": Config.Web.Pprof},
		"prometheus": map[string]interface{}{
			"enabled":          Config.Prometheus.Enabled,
			"host_metrics":     Config.Prometheus.HostMetrics,
			"latency_metrics":  Config.Prometheus.LatencyMetrics,
			"attribute_labels": Config.Prometheus.AttributeLabels,
		},
		"redis": map[string]interface{}{
			"enabled":     Config.Redis.Enabled,
			"addr":        Config.Redis.Addr,
			"db":          Config.Redis.DB,
			"events_key":  Config.Redis.EventsKey,
			"reports_key": Config.Redis.ReportsKey,
		},
	}
}

func maskURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || s == "" {
		return s
	}
	if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "token") || strings.Contains(lk, "key") || strings.Contains(lk, "pass") || strings.Contains(lk, "secret") {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func LoadConfig(path string, Config *Configuration) error {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	expandedContent := ExpandEnvDefault(string(bytes))
	if err := yaml.Unmarshal([]byte(expandedContent), Config); err != nil {
		return err
	}
	fmt.Printf("Loaded configuration from %v\n", path)
	return nil
}

func ExpandEnvDefault(s string) string {
	return os.Expand(s, func(key string) string {
		// Определение дефолтного значения через двоеточие.
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 {
			if value, ok := os.LookupEnv(parts[0]); ok {
				return value
			}
			return parts[1]
		}
		return os.Getenv(key)
	})
}

func InitializeLogger() {
	lg = logrus.New()
	lg.SetOutput(os.Stdout)
	level := logrus.InfoLevel
	if v := strings.TrimSpace(Config.Log.Level); v != "" {
		l, err := logrus.ParseLevel(v)
		if err != nil {
			log.Fatalf("Unknown log.level '%v' (debug, info, warn, error)", v)
		}
		level = l
	}
	lg.SetLevel(level)
	if strings.EqualFold(Config.Log.Format, "json") {
		lg.SetFormatter(&logrus.JSONFormatter{})
	} else {
		lg.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, DisableColors: true})
	}
	if lg.IsLevelEnabled(logrus.DebugLevel) {
		lg.SetReportCaller(true)
	}
}
