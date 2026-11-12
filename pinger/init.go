package pinger

import (
	"fmt"
	"math"
	"math/rand"
	"net"
	"sync/atomic"
	"time"

	"github.com/meklis/all-ok-pinger/prom"
	"github.com/sirupsen/logrus"
)

const (
	// StatusUp - status of host which received all packets
	StatusUp = 100

	// count of goroutines reading replies from ICMP socket
	readers = 2
	// sending rate of one writer (own socket), packets/s. Measured ~10k/s, with reserve
	packetsPerWriter = 8000
	// writers if sending rate is not limited
	unlimitedWriters = 8
	// receive buffer of ICMP socket. With default buffer (208KB) replies are dropped by kernel
	socketReadBuffer = 32 << 20
)

func NewPinger(conf Configuration, lg *logrus.Logger) (*Pinger, error) {
	ic := &conf.ICMP
	if ic.Inspections <= 0 {
		ic.Inspections = 10
	}
	if ic.Packets <= 0 {
		ic.Packets = 4
	}
	if ic.Inspections*ic.Packets > 0xffff {
		return nil, fmt.Errorf("icmp.inspections * icmp.packets must be <= 65535")
	}
	if ic.SuccessForUp <= 0 {
		ic.SuccessForUp = 2
	}
	if ic.FailedForDown <= 0 {
		ic.FailedForDown = ic.Inspections
	}
	if ic.SuccessForUp > ic.Inspections || ic.FailedForDown > ic.Inspections {
		return nil, fmt.Errorf("icmp.success_for_up and icmp.failed_for_down must be <= icmp.inspections (%v)", ic.Inspections)
	}
	if ic.ResponseTimeout <= 0 {
		ic.ResponseTimeout = 2 * time.Second
	}
	if conf.TCP.Workers <= 0 {
		conf.TCP.Workers = 50
	}
	if conf.TCP.Timeout <= 0 {
		conf.TCP.Timeout = 5 * time.Second
	}

	c := &Pinger{
		Config:     conf,
		lg:         lg,
		chanReq:    make(chan sendJob, 10000),
		chanTcpReq: make(chan *hostCycle, 50),
		hosts:      map[string]*hostCycle{},
		icmpID:     rand.New(rand.NewSource(time.Now().UnixNano())).Intn(0xffff) + 1,
	}
	if pps := ic.PacketsPerSecond; pps > 0 {
		c.writers = (pps + packetsPerWriter - 1) / packetsPerWriter
		c.sendInterval = time.Duration(float64(time.Second) * float64(c.writers) / float64(pps))
		lg.Infof("ICMP sending rate: %v packets/s (%v writers)", pps, c.writers)
	} else {
		c.writers = unlimitedWriters
		lg.Infof("ICMP sending rate is not limited (%v writers)", c.writers)
	}

	if err := c.reopenSocket(false, nil); err != nil {
		return nil, fmt.Errorf("error opening ICMP socket: %v", err)
	}
	if ic.IPv6 {
		if err := c.reopenSocket(true, nil); err != nil {
			return nil, fmt.Errorf("error opening ICMP6 socket: %v", err)
		}
	}
	for i := 0; i < readers; i++ {
		go c.readSocket(false)
		if ic.IPv6 {
			go c.readSocket(true)
		}
	}
	for i := 0; i < c.writers; i++ {
		go c.writeSocket()
	}
	if conf.TCP.Enabled {
		for i := 0; i < conf.TCP.Workers; i++ {
			go c.checkingByTCP()
		}
	}
	return c, nil
}

// StartPing - run one ping cycle over the devices list.
// Returns result for every device from the list (in the same order).
//
// Principle: in every inspection all hosts of the inspection get `packets` echo requests (round-robin,
// in random order). Host which confirmed its status (up and responding, down and not responding) leaves the cycle,
// other hosts are inspected again after inspection_interval:
//   - up -> down after failed_for_down failed inspections in a row (then TCP check, if enabled - 999);
//   - down -> up after success_for_up success inspections in a row.
//
// Status of up host - percent of received packets in cycle (1..100, 100 - all packets received, even in one inspection),
// down - 0, not responding over ICMP but accepting TCP connection - 999.
// Host with unknown status is checked as up.
func (c *Pinger) StartPing(data []Device) []Result {
	conf := c.Config.ICMP
	now := time.Now()

	hosts := make(map[string]*hostCycle, len(data))
	byIndex := make([]*hostCycle, len(data))
	results := make([]Result, len(data))
	for i, dev := range data {
		results[i].Device = dev
		results[i].Status = dev.Status
		results[i].CheckedAt = now
		ip := net.ParseIP(dev.Ip)
		if ip == nil {
			results[i].Error = "invalid IP address"
			c.lg.Warnf("Host '%v' has invalid IP address, ignoring", dev.Ip)
			continue
		}
		v6 := ip.To4() == nil
		if v6 && !conf.IPv6 {
			results[i].Error = "IPv6 is disabled in configuration"
			continue
		}
		key := ip.String()
		if h, exist := hosts[key]; exist {
			byIndex[i] = h
			continue
		}
		h := &hostCycle{
			dev:       dev,
			key:       key,
			addr:      &net.IPAddr{IP: ip},
			ipv6:      v6,
			up:        dev.Status == nil || *dev.Status > 0,
			seen:      make(map[uint16]struct{}, conf.Packets),
			inRequest: true,
		}
		hosts[key] = h
		byIndex[i] = h
	}

	// random order of hosts - spread load between network segments
	order := make([]*hostCycle, 0, len(hosts))
	for _, h := range hosts {
		order = append(order, h)
	}
	rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	prom.CycleStarted()
	c.mu.Lock()
	c.cycleID++
	cycle := c.cycleID
	c.inspection = 0
	c.hosts = hosts
	c.mu.Unlock()

	for inspect := 0; inspect < conf.Inspections; inspect++ {
		request := make([]*hostCycle, 0, len(hosts))
		c.mu.Lock()
		c.inspection = inspect
		for _, h := range order {
			if h.inRequest {
				h.inspResp = 0
				request = append(request, h)
			}
		}
		c.mu.Unlock()
		if len(request) == 0 {
			break
		}
		if inspect > 0 {
			time.Sleep(conf.InspectionInterval)
		}
		prom.CountInspectionsInc()
		inspStart := time.Now()

		// Round-robin: packet N is sent to all hosts, then packet N+1 (like fping)
		sendStart := time.Now()
		c.sendWg.Add(len(request) * conf.Packets)
		for i := 0; i < conf.Packets; i++ {
			for _, h := range request {
				c.chanReq <- sendJob{h: h, seq: inspect*conf.Packets + i, cycle: cycle}
			}
		}
		c.sendWg.Wait()
		c.lg.Debugf("Inspection %v: sent %v packets in %v", inspect+1, len(request)*conf.Packets, time.Since(sendStart).Round(time.Millisecond))
		time.Sleep(conf.ResponseTimeout)

		c.mu.Lock()
		responded := 0
		var states []InspectionState
		for _, h := range request {
			resp := h.inspResp > 0
			if resp {
				responded++
			}
			switch {
			case h.up && resp, !h.up && !resp:
				// status confirmed
				h.inRequest = false
			case h.up:
				h.failedInsp++
				h.inRequest = h.failedInsp < conf.FailedForDown
			default:
				h.successInsp++
				h.inRequest = h.successInsp < conf.SuccessForUp
			}
			if c.OnInspection != nil && h.dev.Status != nil && h.failedInsp+h.successInsp > 0 {
				states = append(states, InspectionState{Ip: h.dev.Ip, Status: h.dev.Status, Failed: h.failedInsp, Success: h.successInsp})
			}
		}
		c.mu.Unlock()
		prom.Inspection(inspect+1, len(request), time.Since(inspStart).Seconds())
		c.lg.Infof("Inspection %v: received responses from %v of %v hosts", inspect+1, responded, len(request))
		if c.OnInspection != nil {
			c.OnInspection(inspect+1, states)
		}
	}

	c.mu.Lock()
	var forDown []*hostCycle
	for _, h := range hosts {
		if h.up && h.failedInsp >= conf.FailedForDown {
			forDown = append(forDown, h)
		}
	}
	c.mu.Unlock()

	if c.Config.TCP.Enabled && len(c.Config.TCP.Ports) > 0 && len(forDown) > 0 {
		c.lg.Debugf("TCP checking of %v hosts", len(forDown))
		c.tcpWg.Add(len(forDown))
		for _, h := range forDown {
			c.chanTcpReq <- h
		}
		c.tcpWg.Wait()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	up, down := 0, 0
	for i := range results {
		h := byIndex[i]
		if h == nil {
			continue
		}
		r := &results[i]
		r.Transmitted = int(atomic.LoadInt32(&h.sent))
		r.Received = h.received
		r.RttCount = h.rttCount
		r.RttMin = h.rttMin
		r.RttMax = h.rttMax
		r.RttSum = h.rttSum
		r.ICMP = h.received > 0
		if h.tcpChecked {
			ok := h.tcpOk
			r.TCP = &ok
		}

		var status int
		switch {
		case h.up && h.failedInsp >= conf.FailedForDown:
			status = StatusDown
			if h.tcpOk {
				status = StatusTcpUp
			}
		case h.up, h.successInsp >= conf.SuccessForUp:
			// host is up: percent of received packets in cycle
			status = receivedPercent(r.Transmitted, r.Received)
		default:
			// was down and not risen
			status = StatusDown
		}
		r.Status = &status
		r.Changed = r.Device.Status == nil || *r.Device.Status != status
		if r.Changed && status > 0 {
			up++
		} else if r.Changed {
			down++
		}
	}
	c.lg.Infof("ICMP cycle finished: up=%v, down=%v (changed statuses)", up, down)
	// cycle finished, late responses are ignored
	c.cycleID++
	c.hosts = map[string]*hostCycle{}
	return results
}

// receivedPercent - status of up host: percent of received packets (1..100).
// Host which is up has status > 0, so result is at least 1.
func receivedPercent(transmitted, received int) int {
	if transmitted <= 0 {
		return StatusUp
	}
	p := int(math.Round(float64(received) * 100 / float64(transmitted)))
	if p > 100 {
		p = 100
	}
	if p < 1 {
		p = 1
	}
	return p
}
