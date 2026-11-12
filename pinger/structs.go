package pinger

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// AttributePrefix - prefix of additional host attributes in the host list (_id, _name, _location...)
const AttributePrefix = "_"

// TCP status value, used when host is not responding to ICMP, but TCP connection is established
const StatusTcpUp = 999

// Status of host which is down (in all modes)
const StatusDown = 0

type Pinger struct {
	lg     *logrus.Logger
	Config Configuration

	// sendInterval - interval between packets of one writer (0 - not limited)
	sendInterval time.Duration
	writers      int

	chanReq    chan sendJob
	chanTcpReq chan *hostCycle

	sockMu      sync.RWMutex
	icmpSocket  *net.IPConn
	icmp6Socket *net.IPConn

	// icmpID - identifier of ICMP echo packets of this pinger instance.
	// Replies with another ID (other processes) are ignored.
	icmpID int

	// OnInspection - called after every inspection (1-based) with hosts which have failed (status up)
	// or success (status down) inspections - for realtime states
	OnInspection func(inspection int, states []InspectionState)

	sendWg sync.WaitGroup
	tcpWg  sync.WaitGroup

	// Current cycle data. Protected by mu
	mu         sync.Mutex
	cycleID    uint32
	inspection int
	hosts      map[string]*hostCycle
}

type Configuration struct {
	ICMP ICMPConfig
	TCP  TCPConfig
}

type ICMPConfig struct {
	// Inspections - max count of inspections in cycle
	Inspections int `yaml:"inspections"`
	// SuccessForUp - success inspections in a row for down -> up
	SuccessForUp int `yaml:"success_for_up"`
	// FailedForDown - failed inspections in a row for up -> down
	FailedForDown int `yaml:"failed_for_down"`
	// Packets - echo requests to host in every inspection, host is responding if at least one reply received
	Packets int `yaml:"packets"`
	// PacketsPerSecond - total sending rate, 0 - not limited
	PacketsPerSecond int `yaml:"packets_per_second"`
	// ResponseTimeout - waiting of replies after sending of all packets of inspection
	ResponseTimeout time.Duration `yaml:"response_timeout"`
	// InspectionInterval - pause between inspections
	InspectionInterval time.Duration `yaml:"inspection_interval"`
	IPv6               bool          `yaml:"ipv6"`
}

type TCPConfig struct {
	// Enabled - hosts which are going down are checked over TCP, status 999 if connected
	Enabled bool          `yaml:"enabled"`
	Ports   []int         `yaml:"ports"`
	Workers int           `yaml:"workers"`
	Timeout time.Duration `yaml:"timeout"`
}

// Device - host from the host list.
// Status == nil means status is unknown (status: null in host list) - report will be sent in any case.
// Attributes - additional fields with prefix "_" (_id, _name, _location...), keys are stored with prefix.
type Device struct {
	Ip          string                 `json:"ip"`
	Status      *int                   `json:"status"`
	TargetGroup string                 `json:"target_group"`
	Attributes  map[string]interface{} `json:"-"`
	// StatusFromState - status in host list was null and filled from previous pass
	StatusFromState bool `json:"-"`
}

func (d *Device) UnmarshalJSON(data []byte) error {
	raw := map[string]json.RawMessage{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	*d = Device{}
	if v, ok := raw["ip"]; ok {
		if err := json.Unmarshal(v, &d.Ip); err != nil {
			return err
		}
		d.Ip = strings.TrimSpace(d.Ip)
	}
	if v, ok := raw["status"]; ok && string(v) != "null" {
		var st json.Number
		if err := json.Unmarshal(v, &st); err != nil {
			// status may be sent as string
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return err
			}
			st = json.Number(s)
		}
		if st != "" {
			f, err := st.Float64()
			if err != nil {
				return err
			}
			i := int(f)
			d.Status = &i
		}
	}
	if v, ok := raw["target_group"]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &d.TargetGroup); err != nil {
			return err
		}
	}
	for k, v := range raw {
		if !strings.HasPrefix(k, AttributePrefix) {
			continue
		}
		var val interface{}
		vd := json.NewDecoder(bytes.NewReader(v))
		vd.UseNumber()
		if err := vd.Decode(&val); err != nil {
			return err
		}
		if d.Attributes == nil {
			d.Attributes = map[string]interface{}{}
		}
		d.Attributes[k] = val
	}
	return nil
}

func (d Device) MarshalJSON() ([]byte, error) {
	m := make(map[string]interface{}, len(d.Attributes)+3)
	for k, v := range d.Attributes {
		m[k] = v
	}
	m["ip"] = d.Ip
	m["status"] = d.Status
	m["target_group"] = d.TargetGroup
	return json.Marshal(m)
}

// InspectionState - intermediate state of host inside of cycle
type InspectionState struct {
	Ip      string
	Status  *int
	Failed  int
	Success int
}

// Result - result of checking one host in the cycle
type Result struct {
	Device Device

	// Status - current status of host after cycle
	Status *int
	// Changed - status differs from status in host list (or status in host list is null)
	Changed bool

	ICMP bool
	// TCP - nil if TCP checking disabled or host wasn't checked over TCP
	TCP *bool

	Transmitted int
	Received    int

	RttCount int
	RttMin   time.Duration
	RttMax   time.Duration
	RttSum   time.Duration

	CheckedAt time.Time
	// Error - host can't be checked (invalid IP address for example)
	Error string
}

// hostCycle - internal state of host inside of ping cycle
type hostCycle struct {
	dev  Device
	key  string // normalized IP address
	addr *net.IPAddr
	ipv6 bool

	sent     int32
	received int
	// responses in current inspection
	inspResp int
	// received sequences, for deduplicate responses
	seen map[uint16]struct{}

	rttCount int
	rttMin   time.Duration
	rttMax   time.Duration
	rttSum   time.Duration

	// up - known status of host (unknown status is checked as up)
	up bool
	// count of failed (for up host) / success (for down host) inspections in a row
	failedInsp  int
	successInsp int
	inRequest   bool
	tcpChecked  bool
	tcpOk       bool
}
