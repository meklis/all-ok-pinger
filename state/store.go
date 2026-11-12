// Package state keeps current state of hosts: last statuses, latency and loss with sliding windows.
package state

import (
	"encoding/json"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/meklis/all-ok-pinger/pinger"
)

const historyDepth = 5 * time.Minute

type Latency struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	Avg float64 `json:"avg"`
}

type Loss struct {
	Transmitted int     `json:"transmitted"`
	Received    int     `json:"received"`
	Loss        float64 `json:"loss"`
}

// HostState - current state of host. Used for reports, API and metrics.
type HostState struct {
	Ip          string
	TargetGroup string
	Attributes  map[string]interface{}
	Status      *int
	// ListStatus - status from host list (status on the server side), nil if status: null
	ListStatus *int
	// ReportedStatus - last status successfully sent in report
	ReportedStatus *int
	// ListLoadedAt - time of loading of ListStatus
	ListLoadedAt time.Time
	// ReportPending - status of host differs from status known by server (sent in report or from host list),
	// host will be sent in the next report
	ReportPending bool
	ICMP          bool
	TCP           *bool
	Latency       *Latency
	Latency1m     *Latency
	Latency5m     *Latency
	Loss          Loss
	Loss1m        Loss
	Loss5m        Loss
	Error         string

	CheckedAt  time.Time
	ChangedAt  time.Time
	ReportedAt time.Time
	// AddedAt - time when host was added from host list
	AddedAt time.Time

	// Pending - realtime state inside of current cycle: falling (was up, not responding in inspections)
	// or rising (was down, started responding). Empty - no pending changes
	Pending *Pending

	// cumulative counters
	// Flaps - count of up <-> down changes since start
	Flaps            uint64
	FlappedAt        time.Time
	TotalTransmitted uint64
	TotalReceived    uint64
	Cycles           uint64

	history []cycleSample
}

const (
	StateUp      = "up"
	StateDown    = "down"
	StateUnknown = "unknown"
	StateFalling = "falling"
	StateRising  = "rising"
)

type Pending struct {
	State              string    `json:"state"`
	FailedInspections  int       `json:"failed_inspections"`
	SuccessInspections int       `json:"success_inspections"`
	Inspection         int       `json:"inspection"`
	Inspections        int       `json:"inspections"`
	Since              time.Time `json:"since"`
}

// State - current state of host: up, down, unknown or realtime falling/rising
func (h *HostState) State() string {
	if h.Pending != nil {
		return h.Pending.State
	}
	switch {
	case h.Status == nil:
		return StateUnknown
	case *h.Status > 0:
		return StateUp
	default:
		return StateDown
	}
}

type cycleSample struct {
	at          time.Time
	transmitted int
	received    int
	rttCount    int
	rttMin      time.Duration
	rttMax      time.Duration
	rttSum      time.Duration
}

// ReportObject - object for reporting to API / response of web API
func (h *HostState) ReportObject() map[string]interface{} {
	m := make(map[string]interface{}, len(h.Attributes)+12)
	for k, v := range h.Attributes {
		m[k] = v
	}
	m["ip"] = h.Ip
	m["status"] = h.Status
	if h.TargetGroup != "" {
		m["target_group"] = h.TargetGroup
	}
	m["icmp"] = h.ICMP
	m["tcp"] = h.TCP
	m["latency"] = h.Latency
	m["latency_1m"] = h.Latency1m
	m["latency_5m"] = h.Latency5m
	m["loss"] = h.Loss
	return m
}

// ApiObject - extended object for web API (report object + service info)
func (h *HostState) ApiObject() map[string]interface{} {
	m := h.ReportObject()
	m["state"] = h.State()
	m["pending"] = h.Pending
	m["list_status"] = h.ListStatus
	m["reported_status"] = h.ReportedStatus
	m["report_pending"] = h.ReportPending
	m["loss_1m"] = h.Loss1m
	m["loss_5m"] = h.Loss5m
	m["checked_at"] = nullTime(h.CheckedAt)
	m["changed_at"] = nullTime(h.ChangedAt)
	m["reported_at"] = nullTime(h.ReportedAt)
	m["cycles"] = h.Cycles
	m["flaps"] = h.Flaps
	m["flapped_at"] = nullTime(h.FlappedAt)
	if h.Error != "" {
		m["error"] = h.Error
	} else {
		m["error"] = nil
	}
	return m
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339)
}

func (h HostState) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.ApiObject())
}

// IsUp - host is working now
func (h *HostState) IsUp() bool {
	return h.Status != nil && *h.Status > 0
}

type CycleInfo struct {
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	DurationSec float64   `json:"duration_sec"`
	// Flaps - hosts changed state up <-> down (excluding hosts without previous status)
	Flaps       int    `json:"flaps"`
	Hosts       int    `json:"hosts"`
	Changed     int    `json:"changed"`
	ReportSent  int    `json:"report_sent"`
	ReportError string `json:"report_error"`
}

// Progress - progress of current ping cycle
type Progress struct {
	Running     bool      `json:"running"`
	StartedAt   time.Time `json:"started_at"`
	Inspection  int       `json:"inspection"`
	Inspections int       `json:"inspections"`
	Hosts       int       `json:"hosts"`
	Falling     int       `json:"falling"`
	Rising      int       `json:"rising"`
}

// HostListInfo - info about loading of host list
type HostListInfo struct {
	LoadedAt time.Time `json:"loaded_at"`
	Hosts    int       `json:"hosts"`
	Added    int       `json:"added"`
	Updated  int       `json:"updated"`
	Removed  int       `json:"removed"`
	Error    string    `json:"error"`
	ErrorAt  time.Time `json:"error_at"`
}

// Event - change of host state (status changed, falling, rising...)
type Event struct {
	Type       string                 `json:"type"`
	Ip         string                 `json:"ip"`
	Time       time.Time              `json:"time"`
	Status     *int                   `json:"status"`
	PrevStatus *int                   `json:"prev_status"`
	State      string                 `json:"state"`
	Pending    *Pending               `json:"pending,omitempty"`
	Host       map[string]interface{} `json:"host"`
}

const (
	EventStatusChanged    = "status_changed"
	EventFalling          = "falling"
	EventRising           = "rising"
	EventFallingCancelled = "falling_cancelled"
	EventRisingCancelled  = "rising_cancelled"
	EventHostAdded        = "host_added"
	EventHostRemoved      = "host_removed"
)

// RealtimeConfig - thresholds for realtime states
type RealtimeConfig struct {
	// FallingAfterInspections - host with status up is falling after N failed inspections in cycle
	FallingAfterInspections int `yaml:"falling_after_inspections"`
	// RisingAfterInspections - host with status down is rising after N success inspections in cycle
	RisingAfterInspections int `yaml:"rising_after_inspections"`
}

type Store struct {
	mu        sync.RWMutex
	hosts     map[string]*HostState
	order     []string
	cycles    uint64
	lastCycle CycleInfo
	progress  Progress
	hostList  HostListInfo
	startedAt time.Time
	realtime  RealtimeConfig
	onEvent   []func(Event)

	// reports: enabled and statuses from host list are ignored
	reportEnabled bool
	ignoreList    bool
}

// SetReporting - reports are enabled; statuses from host list are ignored (as status: null)
func (s *Store) SetReporting(enabled, ignoreListStatuses bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportEnabled, s.ignoreList = enabled, ignoreListStatuses
	for _, h := range s.hosts {
		s.refreshPending(h)
	}
}

// serverStatus - status known by server: the newest of last sent status and status from host list
// (status: null in host list means, that server doesn't send status). nil - server status is unknown
func (s *Store) serverStatus(h *HostState) *int {
	list := h.ListStatus
	if s.ignoreList {
		list = nil
	}
	switch {
	case list == nil:
		return h.ReportedStatus
	case h.ReportedStatus == nil, h.ListLoadedAt.After(h.ReportedAt):
		return list
	default:
		return h.ReportedStatus
	}
}

// refreshPending - host must be reported, if status of pinger differs from status known by server
func (s *Store) refreshPending(h *HostState) {
	h.ReportPending = s.reportEnabled && h.Status != nil && h.Error == "" && !intPtrEqual(h.Status, s.serverStatus(h))
}

// ReportPending - hosts which must be sent in report
func (s *Store) ReportPending() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]string, 0)
	for _, ip := range s.order {
		if s.hosts[ip].ReportPending {
			res = append(res, ip)
		}
	}
	return res
}

func NewStore() *Store {
	return &Store{hosts: map[string]*HostState{}, startedAt: time.Now(),
		realtime: RealtimeConfig{FallingAfterInspections: 3, RisingAfterInspections: 1}}
}

func (s *Store) SetRealtimeConfig(c RealtimeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.FallingAfterInspections <= 0 {
		c.FallingAfterInspections = 3
	}
	if c.RisingAfterInspections <= 0 {
		c.RisingAfterInspections = 1
	}
	s.realtime = c
}

// OnEvent - add handler of events. Handlers are called under store lock, they must not block and must not call store
func (s *Store) OnEvent(f func(Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onEvent = append(s.onEvent, f)
}

func (s *Store) emit(t string, h *HostState, prev *int, now time.Time) {
	s.emitStatus(t, h, h.Status, prev, now)
}

func (s *Store) emitStatus(t string, h *HostState, status, prev *int, now time.Time) {
	if len(s.onEvent) == 0 {
		return
	}
	var pending *Pending
	if h.Pending != nil {
		p := *h.Pending
		pending = &p
	}
	e := Event{Type: t, Ip: h.Ip, Time: now, Status: copyInt(status), PrevStatus: copyInt(prev),
		State: h.State(), Pending: pending, Host: h.ReportObject()}
	for _, f := range s.onEvent {
		f(e)
	}
}

func copyInt(v *int) *int {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// SyncHosts - apply host list: add new hosts, update attributes of existing, remove missing.
// Statuses of existing hosts are kept (status from list is stored as ListStatus).
func (s *Store) SyncHosts(devices []pinger.Device) HostListInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	info := HostListInfo{LoadedAt: now}
	seen := make(map[string]bool, len(devices))
	order := make([]string, 0, len(devices))
	for _, d := range devices {
		if d.Ip == "" || seen[d.Ip] {
			continue
		}
		seen[d.Ip] = true
		order = append(order, d.Ip)
		h, exist := s.hosts[d.Ip]
		if !exist {
			h = &HostState{Ip: d.Ip, AddedAt: now}
			s.hosts[d.Ip] = h
			info.Added++
		} else if h.TargetGroup != d.TargetGroup || !intPtrEqual(h.ListStatus, d.Status) || !attrsEqual(h.Attributes, d.Attributes) {
			info.Updated++
		}
		h.TargetGroup = d.TargetGroup
		h.Attributes = d.Attributes
		h.ListStatus = d.Status
		h.ListLoadedAt = now
		s.refreshPending(h)
		if !exist && !s.hostList.LoadedAt.IsZero() {
			s.emit(EventHostAdded, h, nil, now)
		}
	}
	for ip, h := range s.hosts {
		if !seen[ip] {
			info.Removed++
			s.emit(EventHostRemoved, h, h.Status, now)
			delete(s.hosts, ip)
		}
	}
	s.order = order
	info.Hosts = len(order)
	s.hostList = info
	return info
}

func attrsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if AttributeValue(v) != AttributeValue(b[k]) {
			return false
		}
	}
	return true
}

func (s *Store) SetHostListError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostList.Error = err.Error()
	s.hostList.ErrorAt = time.Now()
}

func (s *Store) HostListInfo() HostListInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostList
}

// Devices - list of hosts for ping cycle with known status:
// reportEnabled - last reported status (failed reports are retried), otherwise - status of previous pass;
// if there is no status in state - status from host list (if not ignored).
// Devices - hosts for ping cycle. Known status of host is status of previous pass (actual state of pinger),
// for hosts without state - status known by server (last sent or from host list)
func (s *Store) Devices() []pinger.Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]pinger.Device, 0, len(s.order))
	for _, ip := range s.order {
		h := s.hosts[ip]
		d := pinger.Device{Ip: h.Ip, TargetGroup: h.TargetGroup, Attributes: h.Attributes}
		if h.Status != nil {
			d.Status = copyInt(h.Status)
			d.StatusFromState = true
		} else if st := s.serverStatus(h); st != nil {
			d.Status = copyInt(st)
		}
		res = append(res, d)
	}
	return res
}

// StartCycle - mark start of ping cycle
func (s *Store) StartCycle(hosts, inspections int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = Progress{Running: true, StartedAt: time.Now(), Hosts: hosts, Inspections: inspections}
}

// PendingHost - host in transition (falling / rising) in current cycle
type PendingHost struct {
	Ip     string `json:"ip"`
	Status *int   `json:"status"`
	*Pending
}

// PendingList - hosts which are falling or rising now
func (s *Store) PendingList() []PendingHost {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]PendingHost, 0, s.progress.Falling+s.progress.Rising)
	for _, h := range s.hosts {
		if h.Pending != nil {
			p := *h.Pending
			list = append(list, PendingHost{Ip: h.Ip, Status: copyInt(h.Status), Pending: &p})
		}
	}
	return list
}

func (s *Store) Progress() Progress {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.progress
}

// UpdateInspection - realtime states after inspection
func (s *Store) UpdateInspection(inspection int, states []pinger.InspectionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.progress.Inspection = inspection
	for _, st := range states {
		h, ok := s.hosts[st.Ip]
		if !ok || st.Status == nil {
			continue
		}
		state := ""
		switch {
		case *st.Status > 0 && st.Failed >= s.realtime.FallingAfterInspections:
			state = StateFalling
		case *st.Status <= 0 && st.Success >= s.realtime.RisingAfterInspections:
			state = StateRising
		}
		if state == "" {
			continue
		}
		isNew := h.Pending == nil || h.Pending.State != state
		if isNew {
			h.Pending = &Pending{State: state, Since: now}
		}
		h.Pending.FailedInspections = st.Failed
		h.Pending.SuccessInspections = st.Success
		h.Pending.Inspection = inspection
		h.Pending.Inspections = s.progress.Inspections
		if isNew {
			if state == StateFalling {
				s.emitStatus(EventFalling, h, st.Status, st.Status, now)
			} else {
				s.emitStatus(EventRising, h, st.Status, st.Status, now)
			}
		}
	}
	s.progress.Falling, s.progress.Rising = 0, 0
	for _, h := range s.hosts {
		if h.Pending == nil {
			continue
		}
		if h.Pending.State == StateFalling {
			s.progress.Falling++
		} else {
			s.progress.Rising++
		}
	}
}

// Update - apply results of ping cycle. Hosts removed from host list during cycle are ignored.
func (s *Store) Update(results []pinger.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	done := make(map[string]bool, len(results))
	for _, r := range results {
		ip := r.Device.Ip
		if done[ip] {
			continue
		}
		done[ip] = true
		h, exist := s.hosts[ip]
		if !exist {
			continue
		}
		// previous status - status in state or known status of cycle (from host list)
		prevStatus := h.Status
		if prevStatus == nil {
			prevStatus = r.Device.Status
		}
		pending := h.Pending
		h.Pending = nil
		h.Status = r.Status
		h.Error = r.Error
		h.ICMP = r.ICMP
		h.TCP = r.TCP
		h.CheckedAt = r.CheckedAt
		if prevStatus != nil && h.Status != nil && (*prevStatus > 0) != (*h.Status > 0) {
			h.Flaps++
			h.FlappedAt = now
		}
		statusChanged := !intPtrEqual(prevStatus, h.Status)
		if statusChanged {
			h.ChangedAt = now
		}
		s.refreshPending(h)
		if r.Error == "" {
			h.Cycles++
			h.TotalTransmitted += uint64(r.Transmitted)
			h.TotalReceived += uint64(r.Received)
			h.history = append(h.history, cycleSample{
				at:          now,
				transmitted: r.Transmitted,
				received:    r.Received,
				rttCount:    r.RttCount,
				rttMin:      r.RttMin,
				rttMax:      r.RttMax,
				rttSum:      r.RttSum,
			})
		}
		// drop old history
		cut := 0
		for cut < len(h.history) && now.Sub(h.history[cut].at) > historyDepth {
			cut++
		}
		if cut > 0 {
			h.history = append([]cycleSample(nil), h.history[cut:]...)
		}
		var last []cycleSample
		if r.Error == "" && len(h.history) > 0 {
			last = h.history[len(h.history)-1:]
		}
		h.Latency, h.Loss = aggregate(last)
		h.Latency1m, h.Loss1m = aggregate(window(h.history, now, time.Minute))
		h.Latency5m, h.Loss5m = aggregate(window(h.history, now, 5*time.Minute))

		// events: pending state was not confirmed (host is still up / down, percent of status may change),
		// status changed (except first status of host)
		if pending != nil && (prevStatus == nil || h.Status == nil || (*prevStatus > 0) == (*h.Status > 0)) {
			if pending.State == StateFalling {
				s.emit(EventFallingCancelled, h, prevStatus, now)
			} else {
				s.emit(EventRisingCancelled, h, prevStatus, now)
			}
		}
		if statusChanged && prevStatus != nil {
			s.emit(EventStatusChanged, h, prevStatus, now)
		}
	}
	// pending states of hosts without results
	for _, h := range s.hosts {
		h.Pending = nil
	}
	s.progress.Running = false
	s.progress.Falling, s.progress.Rising = 0, 0
}

// MarkReported - set reported time and reported status for hosts
func (s *Store) MarkReported(ips []string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ip := range ips {
		if h, ok := s.hosts[ip]; ok {
			h.ReportedAt = at
			if h.Status != nil {
				st := *h.Status
				h.ReportedStatus = &st
			}
			s.refreshPending(h)
		}
	}
}

func (s *Store) SetCycleInfo(info CycleInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !info.FinishedAt.IsZero() {
		s.cycles++
	}
	s.lastCycle = info
}

func (s *Store) CycleInfo() (CycleInfo, uint64, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastCycle, s.cycles, s.startedAt
}

// Get - copy of host state
func (s *Store) Get(ip string) (HostState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[ip]
	if !ok {
		return HostState{}, false
	}
	return h.copy(), true
}

// List - copy of all hosts states, in host list order
func (s *Store) List() []HostState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]HostState, 0, len(s.order))
	for _, ip := range s.order {
		res = append(res, s.hosts[ip].copy())
	}
	return res
}

func (h *HostState) copy() HostState {
	c := *h
	c.history = nil
	if h.Pending != nil {
		p := *h.Pending
		c.Pending = &p
	}
	return c
}

func window(history []cycleSample, now time.Time, d time.Duration) []cycleSample {
	idx := sort.Search(len(history), func(i int) bool { return now.Sub(history[i].at) <= d })
	return history[idx:]
}

func aggregate(samples []cycleSample) (*Latency, Loss) {
	loss := Loss{}
	var count int
	var min, max, sum time.Duration
	for _, s := range samples {
		loss.Transmitted += s.transmitted
		loss.Received += s.received
		if s.rttCount == 0 {
			continue
		}
		if count == 0 || s.rttMin < min {
			min = s.rttMin
		}
		if s.rttMax > max {
			max = s.rttMax
		}
		sum += s.rttSum
		count += s.rttCount
	}
	if loss.Transmitted > 0 {
		received := loss.Received
		if received > loss.Transmitted {
			received = loss.Transmitted
		}
		loss.Loss = round(float64(loss.Transmitted-received)*100/float64(loss.Transmitted), 2)
	}
	if count == 0 {
		return nil, loss
	}
	return &Latency{
		Min: ms(min),
		Max: ms(max),
		Avg: ms(sum / time.Duration(count)),
	}, loss
}

func ms(d time.Duration) float64 {
	return round(float64(d)/float64(time.Millisecond), 3)
}

func round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
