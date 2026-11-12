package prom

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// System metrics of pinger. Per host metrics are exported by state.Collector
var (
	promCountCicles = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_cicles_count",
		Help: "Count of ping cicles for pinger",
	}, []string{})
	promCountInspections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_inspections_count",
		Help: "Count of ping inspections for pinger",
	}, []string{})
	promSysInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_version",
		Help: "Version of pinger",
	}, []string{"version", "build_date"})
	promCicleTime = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "icmp_pinger_sys_cicles_sec",
		Help: "Summary time of ping cicle",
	}, []string{})
	promLastCicleTime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_last_cicle_sec",
		Help: "Duration of last ping cicle",
	})
	promReports = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "icmp_pinger_sys_reports_count",
		Help: "Count of reports sent to API",
	}, []string{"result"})
	promReportedHosts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "icmp_pinger_sys_reported_hosts_count",
		Help: "Count of hosts sent in reports to API",
	})
	promHostListErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "icmp_pinger_sys_host_list_errors_count",
		Help: "Count of errors loading host list from API",
	})
	PromEnabled bool
)

func CountCiclesInc() {
	if !PromEnabled {
		return
	}
	promCountCicles.With(map[string]string{}).Inc()
}

func CountInspectionsInc() {
	if !PromEnabled {
		return
	}
	promCountInspections.With(map[string]string{}).Inc()
}

func SysInfo(version string, buildDate string) {
	if !PromEnabled {
		return
	}
	promSysInfo.With(map[string]string{"version": version, "build_date": buildDate}).Set(1)
}

func CicleTimeAdd(durationSec float64) {
	if !PromEnabled {
		return
	}
	promCicleTime.With(map[string]string{}).Add(durationSec)
	promLastCicleTime.Set(durationSec)
}

func ReportSent(hosts int, err error) {
	if !PromEnabled {
		return
	}
	if err != nil {
		promReports.With(map[string]string{"result": "error"}).Inc()
		return
	}
	promReports.With(map[string]string{"result": "success"}).Inc()
	promReportedHosts.Add(float64(hosts))
	promReportLastSuccess.Set(float64(time.Now().Unix()))
}

var (
	promReportPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_report_pending_hosts",
		Help: "Count of hosts with status, which differs from status known by server (waiting for report)",
	})
	promReportLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_report_last_success_timestamp_seconds",
		Help: "Unix time of last successful report",
	})
)

func ReportPending(hosts int) {
	if !PromEnabled {
		return
	}
	promReportPending.Set(float64(hosts))
}

func HostListErrorInc() {
	if !PromEnabled {
		return
	}
	promHostListErrors.Inc()
}

var promRedisMessages = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "icmp_pinger_sys_redis_messages_count",
	Help: "Count of messages to redis by result (sent, error, dropped)",
}, []string{"result"})

func RedisMessage(result string, count int) {
	if !PromEnabled {
		return
	}
	promRedisMessages.With(map[string]string{"result": result}).Add(float64(count))
}

var (
	promInspectionDuration = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_inspection_duration_sec",
		Help: "Duration of inspection in last cycle (sending + waiting of replies), by inspection number",
	}, []string{"inspection"})
	promInspectionHosts = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_inspection_hosts",
		Help: "Count of hosts checked in inspection of last cycle, by inspection number",
	}, []string{"inspection"})
	promLastCycleTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_last_cycle_timestamp_seconds",
		Help: "Unix time of finishing of last cycle",
	})
	promHostListLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_host_list_last_success_timestamp_seconds",
		Help: "Unix time of last successful loading of host list",
	})
	promHostListLoadDuration = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_host_list_load_duration_sec",
		Help: "Duration of last successful loading of host list",
	})
	promHostListHosts = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_host_list_hosts",
		Help: "Count of hosts in host list",
	})
)

// CycleStarted - reset inspection metrics of previous cycle (cycle may have less inspections)
func CycleStarted() {
	if !PromEnabled {
		return
	}
	promInspectionDuration.Reset()
	promInspectionHosts.Reset()
}

func Inspection(number int, hosts int, durationSec float64) {
	if !PromEnabled {
		return
	}
	l := map[string]string{"inspection": strconv.Itoa(number)}
	promInspectionDuration.With(l).Set(durationSec)
	promInspectionHosts.With(l).Set(float64(hosts))
}

func CycleFinished(at time.Time) {
	if !PromEnabled {
		return
	}
	promLastCycleTimestamp.Set(float64(at.Unix()))
}

func HostListLoaded(hosts int, durationSec float64, at time.Time) {
	if !PromEnabled {
		return
	}
	promHostListLastSuccess.Set(float64(at.Unix()))
	promHostListLoadDuration.Set(durationSec)
	promHostListHosts.Set(float64(hosts))
}
