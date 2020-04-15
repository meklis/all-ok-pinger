package prom

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	promPeerLatencyMs = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_peer_latency_sec",
		Help: "Current latency from host. if latency = -1 - host is not responded",
	}, []string{"host"})
	promPeerStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_peer_status",
		Help: "Current status of host",
	}, []string{"host"})
	promCountPeerCicles = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_peer_cicles",
		Help: "Count of ping cicles for host",
	}, []string{"host"})
	promCountCicles = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_cicles_count",
		Help: "Count of ping cicles for pinger",
	}, []string{})
	promCountPeerInspections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_peer_inspections_count",
		Help: "Count of ping inspections for pinger",
	}, []string{"host"})
	promCountInspections = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_inspections_count",
		Help: "Count of ping inspections for pinger",
	}, []string{})
	promCountPingPackages = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "icmp_pinger_peer_ping_packages",
		Help: "Count of sended ping packages",
	}, []string{"host"})
	promCountPongPackages = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "icmp_pinger_peer_pong_packages",
		Help: "Count of sended pong packages from peer",
	}, []string{"host"})
	promSysInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "icmp_pinger_sys_version",
		Help: "Version of pinger",
	}, []string{"version", "build_date"})
	promCicleTime = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "icmp_pinger_sys_cicles_sec",
		Help: "Summary time of ping cicle",
	}, []string{})
	PromEnabled bool
)

func PeerLatencyMsSet(peer string, latency float64)  {
	if !PromEnabled {
		return
	}
	promPeerLatencyMs.With(map[string]string{"host": peer}).Set(latency)
}

func PeerStatusSet(peer string, status float64)  {
	if !PromEnabled {
		return
	}
	promPeerStatus.With(map[string]string{"host": peer}).Set(status)
}

func CountPeerCiclesInc(peer string)  {
	if !PromEnabled {
		return
	}
	promCountPeerCicles.With(map[string]string{"host": peer}).Inc()
}

func CountCiclesInc()  {
	if !PromEnabled {
		return
	}
	promCountCicles.With(map[string]string{}).Inc()
}
func CountInspectionsInc()  {
	if !PromEnabled {
		return
	}
	promCountInspections.With(map[string]string{}).Inc()
}
func CountPeerInspectionsInc(peer string)  {
	if !PromEnabled {
		return
	}
	promCountPeerInspections.With(map[string]string{"host": peer}).Inc()
}
func CountPingPackagesInc(peer string)  {
	if !PromEnabled {
		return
	}
	promCountPingPackages.With(map[string]string{"host": peer}).Inc()
}

func CountPongPackagesInc(peer string)  {
	if !PromEnabled {
		return
	}
	promCountPongPackages.With(map[string]string{"host": peer}).Inc()
}

func SysInfo(version string, buildDate string)  {
	if !PromEnabled {
		return
	}
	promSysInfo.With(map[string]string{"version": version, "build_date": buildDate}).Inc()
}

func CicleTimeAdd(durationSec float64)  {
	if !PromEnabled {
		return
	}
	promCicleTime.With(map[string]string{}).Add(durationSec)
}
