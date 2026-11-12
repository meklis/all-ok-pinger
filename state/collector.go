package state

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/meklis/all-ok-pinger/pinger"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	HostMetricsNone     = "none"
	HostMetricsBasic    = "basic"
	HostMetricsDetailed = "detailed"

	LatencyMetricsNone = "none"
	LatencyMetricsAvg  = "avg"
	LatencyMetricsAll  = "all"

	AttributeLabelsInfo = "info"
	AttributeLabelsAll  = "all"
)

// Collector - prometheus collector of per host metrics.
// Metrics are built from current state on every scrape, so removed hosts disappear from metrics.
// Host attributes (_id, _name...) are exported as labels without prefix (id, name...):
// in icmp_pinger_peer_info (attribute_labels=info) or on every host metric (attribute_labels=all).
type Collector struct {
	store           *Store
	hostMetrics     string
	latencyMetrics  string
	attributeLabels string
}

func NewCollector(store *Store, hostMetrics, latencyMetrics, attributeLabels string) (*Collector, error) {
	if latencyMetrics == "" {
		latencyMetrics = LatencyMetricsAvg
	}
	switch latencyMetrics {
	case LatencyMetricsNone, LatencyMetricsAvg, LatencyMetricsAll:
	default:
		return nil, fmt.Errorf("unknown prometheus.latency_metrics '%v' (none, avg, all)", latencyMetrics)
	}
	if hostMetrics == "" {
		hostMetrics = HostMetricsBasic
	}
	if attributeLabels == "" {
		attributeLabels = AttributeLabelsInfo
	}
	switch hostMetrics {
	case HostMetricsNone, HostMetricsBasic, HostMetricsDetailed:
	default:
		return nil, fmt.Errorf("unknown prometheus.host_metrics '%v' (none, basic, detailed)", hostMetrics)
	}
	switch attributeLabels {
	case AttributeLabelsInfo, AttributeLabelsAll:
	default:
		return nil, fmt.Errorf("unknown prometheus.attribute_labels '%v' (info, all)", attributeLabels)
	}
	return &Collector{store: store, hostMetrics: hostMetrics, latencyMetrics: latencyMetrics, attributeLabels: attributeLabels}, nil
}

// Describe - unchecked collector (label set depends on attributes in host list)
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

var reservedLabels = map[string]bool{"host": true, "target_group": true, "window": true, "stat": true}

// AttributeLabel - convert attribute key (_location) to prometheus label name (location)
func AttributeLabel(key string) string {
	name := strings.TrimPrefix(key, pinger.AttributePrefix)
	b := strings.Builder{}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteRune('_')
			}
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	label := b.String()
	if label == "" || strings.HasPrefix(label, "__") || reservedLabels[label] {
		label = "attr_" + strings.TrimLeft(label, "_")
	}
	return label
}

func AttributeValue(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool, float64, int, int64:
		return fmt.Sprint(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

type labelSet struct {
	names []string       // all label names
	attrs map[string]int // attribute key -> index of label
}

func buildLabelSet(hosts []HostState) labelSet {
	ls := labelSet{attrs: map[string]int{}}
	used := map[string]bool{"host": true, "target_group": true}
	keys := map[string]bool{}
	for _, h := range hosts {
		for k := range h.Attributes {
			keys[k] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	ls.names = []string{"host", "target_group"}
	for _, k := range sorted {
		label := AttributeLabel(k)
		if used[label] {
			continue
		}
		used[label] = true
		ls.attrs[k] = len(ls.names)
		ls.names = append(ls.names, label)
	}
	return ls
}

func (ls labelSet) values(h *HostState) []string {
	vals := make([]string, len(ls.names))
	vals[0] = h.Ip
	vals[1] = h.TargetGroup
	for k, v := range h.Attributes {
		if i, ok := ls.attrs[k]; ok {
			vals[i] = AttributeValue(v)
		}
	}
	return vals
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	hosts := c.store.List()
	dHostCount := prometheus.NewDesc("icmp_pinger_sys_hosts", "Count of hosts by state", []string{"state"}, nil)
	up, down, unknown := 0, 0, 0
	for i := range hosts {
		switch h := &hosts[i]; {
		case h.Status == nil:
			unknown++
		case *h.Status > 0:
			up++
		default:
			down++
		}
	}
	ch <- prometheus.MustNewConstMetric(dHostCount, prometheus.GaugeValue, float64(up), "up")
	ch <- prometheus.MustNewConstMetric(dHostCount, prometheus.GaugeValue, float64(down), "down")
	ch <- prometheus.MustNewConstMetric(dHostCount, prometheus.GaugeValue, float64(unknown), "unknown")
	if c.hostMetrics == HostMetricsNone {
		return
	}

	attrs := buildLabelSet(hosts)
	hostLabels := attrs.names
	if c.attributeLabels == AttributeLabelsInfo {
		hostLabels = []string{"host", "target_group"}
	}
	withLabels := func(extra ...string) []string {
		return append(append([]string{}, hostLabels...), extra...)
	}
	detailed := c.hostMetrics == HostMetricsDetailed
	var (
		dInfo    = prometheus.NewDesc("icmp_pinger_peer_info", "Attributes of host from host list", attrs.names, nil)
		dStatus  = prometheus.NewDesc("icmp_pinger_peer_status", "Current status of host", hostLabels, nil)
		dUp      = prometheus.NewDesc("icmp_pinger_peer_up", "Host is up (1) or down (0)", hostLabels, nil)
		dIcmp    = prometheus.NewDesc("icmp_pinger_peer_icmp", "Host responds to ICMP in last cycle", hostLabels, nil)
		dTcp     = prometheus.NewDesc("icmp_pinger_peer_tcp", "Host accepts TCP connection (only if TCP checking was performed)", hostLabels, nil)
		dLatSec  = prometheus.NewDesc("icmp_pinger_peer_latency_sec", "Average latency of host in last pass, seconds. -1 - host is not responded", hostLabels, nil)
		dLatMs   = prometheus.NewDesc("icmp_pinger_peer_latency_ms", "Latency of host in ms by window (cycle, 1m, 5m) and stat (min, max, avg)", withLabels("window", "stat"), nil)
		dLoss    = prometheus.NewDesc("icmp_pinger_peer_loss_percent", "Packet loss of host in percent by window (cycle, 1m, 5m)", withLabels("window"), nil)
		dPing    = prometheus.NewDesc("icmp_pinger_peer_ping_packages", "Count of sended ping packages", hostLabels, nil)
		dPong    = prometheus.NewDesc("icmp_pinger_peer_pong_packages", "Count of received pong packages from peer", hostLabels, nil)
		dCicles  = prometheus.NewDesc("icmp_pinger_peer_cicles", "Count of ping cicles for host", hostLabels, nil)
		withInfo = c.attributeLabels == AttributeLabelsInfo && len(attrs.attrs) > 0
	)
	for i := range hosts {
		h := &hosts[i]
		var lv []string
		if c.attributeLabels == AttributeLabelsAll {
			lv = attrs.values(h)
		} else {
			lv = []string{h.Ip, h.TargetGroup}
			if withInfo {
				ch <- prometheus.MustNewConstMetric(dInfo, prometheus.GaugeValue, 1, attrs.values(h)...)
			}
		}
		gauge := func(d *prometheus.Desc, v float64) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, lv...)
		}
		gaugeExtra := func(d *prometheus.Desc, v float64, extra ...string) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, append(append(make([]string, 0, len(lv)+2), lv...), extra...)...)
		}
		counter := func(d *prometheus.Desc, v float64) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, lv...)
		}
		if h.Status != nil {
			gauge(dStatus, float64(*h.Status))
			gauge(dUp, b2f(*h.Status > 0))
		}
		gauge(dIcmp, b2f(h.ICMP))
		if c.latencyMetrics != LatencyMetricsNone {
			if h.Latency != nil {
				gauge(dLatSec, h.Latency.Avg/1000)
			} else {
				gauge(dLatSec, -1)
			}
		}
		if c.latencyMetrics == LatencyMetricsAll {
			for _, w := range []struct {
				name string
				lat  *Latency
			}{{"cycle", h.Latency}, {"1m", h.Latency1m}, {"5m", h.Latency5m}} {
				if w.lat != nil {
					gaugeExtra(dLatMs, w.lat.Min, w.name, "min")
					gaugeExtra(dLatMs, w.lat.Max, w.name, "max")
					gaugeExtra(dLatMs, w.lat.Avg, w.name, "avg")
				}
			}
		}
		counter(dPing, float64(h.TotalTransmitted))
		counter(dPong, float64(h.TotalReceived))
		if !detailed {
			if h.Loss.Transmitted > 0 {
				gaugeExtra(dLoss, h.Loss.Loss, "cycle")
			}
			continue
		}
		if h.TCP != nil {
			gauge(dTcp, b2f(*h.TCP))
		}
		counter(dCicles, float64(h.Cycles))
		for _, w := range []struct {
			name string
			loss Loss
		}{{"cycle", h.Loss}, {"1m", h.Loss1m}, {"5m", h.Loss5m}} {
			if w.loss.Transmitted > 0 {
				gaugeExtra(dLoss, w.loss.Loss, w.name)
			}
		}
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
