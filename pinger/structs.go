package pinger

import (
	"github.com/meklis/all-ok-radius-server/logger"
	"golang.org/x/net/icmp"
	"sync"
	"time"
)

type Pinger struct {
	lg                *logger.Logger
	Config            Configuration
	PrometheusEnabled bool
	reqCache          map[string]int
	respChangedCache  map[string]int
	respCache         map[string]int
	chanReq           chan Device
	chanTcpReq        chan Device
	icmpTiming        map[string]IcmpTiming
	icmpSocket        *icmp.PacketConn
	icmp6Socket       *icmp.PacketConn
	sync.Mutex
}

type IcmpTiming struct {
	Start int64
	Stop  int64
}

type Configuration struct {
	FastMode                bool                   `yaml:"fast_mode"`
	ICMP                    ConfigurationIcmpCheck `yaml:"icmp_check"`
	TCP                     ConfigurationTcpCheck  `yaml:"tcp_check"`
	ApproximateHostQuantity int                    `yaml:"approximate_host_quantity"`
}
type ConfigurationIcmpCheck struct {
	CountPackagesToHost          int           `yaml:"count_packages_to_host"`
	NumberOfInspection           int           `yaml:"number_of_inspection"`
	MustPackagesForUp            int           `yaml:"must_packages_for_up"`
	MustInspectionsSuccessForUp  int           `yaml:"must_inspections_success_for_up"`
	MustInspectionsFailedForDown int           `yaml:"must_inspections_failed_for_down"`
	TimeDurationToSend           time.Duration `yaml:"time_duration_to_send"`
	CountListeners               int           `yaml:"count_listeners"`
	CountWriters                 int           `yaml:"count_writers"`
	EnableIPv6                   bool          `yaml:"enable_ipv6"`
	ResponseTimeout              time.Duration `yaml:"response_timeout"`
	InspectionTimeout            time.Duration `yaml:"inspection_timeout"`
}

type ConfigurationTcpCheck struct {
	Enable            bool          `yaml:"enabled"`
	Ports             []int         `yaml:"ports"`
	CountWorkers      int           `yaml:"count_workers"`
	ConnectionTimeout time.Duration `yaml:"connect_timeout"`
}

type Device struct {
	Ip          string `yaml:"ip" json:"ip"`
	Status      int    `yaml:"status" json:"status"`
	TargetGroup string `yaml:"target_group" json:"target_group"`
}
