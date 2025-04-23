package pinger

import (
	"github.com/meklis/all-ok-pinger/prom"
	"github.com/meklis/all-ok-radius-server/logger"
	"github.com/ztrue/tracerr"
	"golang.org/x/net/icmp"
	"time"
)

func (c *Pinger) StartPing(data []Device) (resp []Device) {
	c.lg.DebugF("Запустили пинговалку IPv4/IPv6...")

	//Clear old data, if exists
	c.clearReq().clearChanges().clearResp()

	//Формируем хеш запроса
	for _, dev := range data {
		prom.PeerStatusSet(dev.Ip, float64(dev.Status))
		prom.CountPeerCiclesInc(dev.Ip)
		c.reqCache[dev.Ip] = dev.Status
	}
	for inspect := 0; inspect < c.Config.ICMP.NumberOfInspection; inspect++ {
		prom.CountInspectionsInc()
		if len(c.reqCache) == 0 {
			c.lg.DebugF("No host for checking in inspection num %v, stop ICMP inspections", inspect+1)
			break
		}
		c.lg.DebugF("Start inspection number %v. Now %v hosts for ping in list ", inspect+1, len(c.reqCache))
		//Push hosts for ping
		c.lg.DebugF("Start sending requests to workers")
		for ip, status := range c.reqCache {
			c.chanReq <- Device{
				Ip:     ip,
				Status: status,
			}
			prom.CountPeerInspectionsInc(ip)
		}
		//Wait for response
		for len(c.chanReq) != 0 {
			time.Sleep(time.Millisecond * 20)
		}
		c.lg.DebugF("Finished sending requests, waiting responses timeout...")
		time.Sleep(c.Config.ICMP.ResponseTimeout)

		//Analize responses
		c.lg.DebugF("Start analize responses...")
		c.Lock()
		responses := c.getResponses()

		c.lg.Noticef("Received %v responses from switches", len(responses))
		for ip, status := range c.reqCache {
			countResp := responses[ip]
			if c.Config.FastMode {
				if countResp <= 0 && status <= 0 {
					//Как лежал, так и не прислал ни одного пакета
					c.deleteDevFromRequest(ip)
				} else if status <= 0 && countResp >= c.Config.ICMP.MustPackagesForUp {
					//Лежал, но прислал необходимое количество пакетов и ему запишем плюсик
					c.setChangedResp(ip, 1)
				} else if countResp == 0 && status > 0 {
					//Работал, но не прислал ничего
					c.setChangedResp(ip, -1)
					//					c.lg.InfoF("Host %v has status UP and now not send response in inspection %v, downing...", ip, inspect+1)
				} else if countResp > 0 && status > 0 {
					//Как работал, так и работает
					c.deleteDevFromRequest(ip)
				} else {
					//Не вносим никаких изменений
				}
			} else {
				if countResp <= 0 {
					c.setChangedResp(ip, -1)
				} else if countResp > 0 && countResp < c.Config.ICMP.MustPackagesForUp {
					c.setChangedResp(ip, 0)
				} else {
					c.setChangedResp(ip, 1)
				}
			}
		}
		c.lg.DebugF("Finished analize responses...")
		c.Unlock()
		time.Sleep(c.Config.ICMP.InspectionTimeout)
		c.clearResp()
	}
	c.lg.DebugF("Finished ICMP checking")
	forUp := make(map[string]int)
	forDown := make(map[string]int)
	for ip, inspections := range c.getChanges() {
		if inspections > 0 {
			//Checking for up
			if inspections >= c.Config.ICMP.MustInspectionsSuccessForUp {
				//	count := int((inspections / c.Config.ICMP.NumberOfInspection) * 100)
				forUp[ip] = int((inspections * 100) / c.Config.ICMP.NumberOfInspection)
			} else {
				c.lg.InfoF("Host %v has %v success inspections, must have %v for UP, ignoring...", ip, inspections, c.Config.ICMP.MustInspectionsSuccessForUp)
			}
		} else {
			if absInt(inspections) >= c.Config.ICMP.MustInspectionsFailedForDown {
				forDown[ip] = int((inspections * 100) / c.Config.ICMP.NumberOfInspection)
			} else {
				c.lg.InfoF("Host %v has %v failed inspections, must have %v for DOWN, ignoring...", ip, absInt(inspections), c.Config.ICMP.MustInspectionsFailedForDown)
			}
		}
	}
	//Work with snmp timing
	for dev, latency := range c.getTimings() {
		prom.PeerLatencyMsSet(dev, float64(latency)/float64(time.Second))
	}
	c.lg.Noticef("UP Hosts = %v, DOWN Hosts = %v", len(forUp), len(forDown))
	if c.Config.TCP.Enable {
		c.lg.DebugF("TCP checking for downing hosts enabled, start checking...")
		for ip, inspections := range forDown {
			c.chanTcpReq <- Device{
				Ip:     ip,
				Status: inspections,
			}
		}
		//Waiting for all host will be received to workers
		c.lg.InfoF("Waiting for TCP checking finished...")
		for len(c.chanTcpReq) != 0 {
			time.Sleep(time.Millisecond * 20)
		}
		time.Sleep(time.Duration(len(c.Config.TCP.Ports)) * c.Config.TCP.ConnectionTimeout)

		for host, _ := range c.getResponses() {
			c.lg.InfoF("Success connected to host %v over TCP, deleting from down list", host)
			forUp[host] = 999
			delete(forDown, host)
		}
		c.lg.DebugF("Count hosts for down %v after TCP checking", len(forDown))
		for d, _ := range forDown {
			c.lg.InfoF("%v after TCP checking not responded", d)
		}
	} else {
		c.lg.DebugF("TCP checking is disabled, formate ping result")
	}

	resp = make([]Device, 0, len(forUp)+len(forDown))
	for _, dev := range data {
		if value, exist := forDown[dev.Ip]; exist && dev.Status != value {
			if c.Config.FastMode {
				value = 0
			}
			resp = append(resp, Device{
				Ip:          dev.Ip,
				Status:      value,
				TargetGroup: dev.TargetGroup,
			})
			c.lg.DebugF("Append to response host %v with status DOWN(%v%%)", dev.Ip, value)
		}
		if value, exist := forUp[dev.Ip]; exist && dev.Status != value {
			if c.Config.FastMode && value != 999 {
				value = 100
			}
			resp = append(resp, Device{
				Ip:          dev.Ip,
				Status:      value,
				TargetGroup: dev.TargetGroup,
			})
			c.lg.DebugF("Append to response host %v with status UP(%v%%)", dev.Ip, value)
		}
	}

	return resp
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func NewPinger(conf Configuration, lg *logger.Logger) (err error, pinger *Pinger) {
	pinger = new(Pinger)
	pinger.Config = conf
	pinger.chanReq = make(chan Device, 5000)
	pinger.chanTcpReq = make(chan Device, 50)
	pinger.icmpTiming = map[string]IcmpTiming{}
	pinger.lg = lg
	pinger.reqCache = make(map[string]int, conf.ApproximateHostQuantity)
	pinger.respCache = make(map[string]int, conf.ApproximateHostQuantity)
	pinger.respChangedCache = make(map[string]int, conf.ApproximateHostQuantity)

	if err = pinger.reopenSockets(); err != nil {
		return tracerr.Errorf("Error opening socket: %v", err), nil
	}

	//Init workers - writers and readers
	for i := 0; i < conf.ICMP.CountListeners; i++ {
		go pinger.readSocket()
		pinger.lg.DebugF("Starting ICMP socket reader, ident=%v", i)
		if conf.ICMP.EnableIPv6 {
			go pinger.readSocket6()
		}

	}
	for i := 0; i < conf.ICMP.CountWriters; i++ {
		pinger.lg.DebugF("Starting ICMP socket writer, ident=%v", i)
		go pinger.writeSocket()
	}

	if conf.TCP.Enable {
		pinger.lg.DebugF("TCP checking enabled, starting workers")
		for i := 0; i < conf.TCP.CountWorkers; i++ {
			pinger.lg.DebugF("Starting TCP checker, ident=%v", i)
			go pinger.checkingByTCP()
		}
	}
	return nil, pinger
}

func (c *Pinger) clearResp() *Pinger {
	c.lg.InfoF("Clearing response data...")
	c.Lock()
	defer c.Unlock()
	c.respCache = make(map[string]int, c.Config.ApproximateHostQuantity)
	return c
}
func (c *Pinger) clearChanges() *Pinger {
	c.lg.InfoF("Clearing response data...")
	c.Lock()
	defer c.Unlock()
	c.respChangedCache = make(map[string]int, c.Config.ApproximateHostQuantity)
	return c
}

func (c *Pinger) clearReq() *Pinger {
	c.lg.InfoF("Clearing request data...")
	c.Lock()
	defer c.Unlock()
	c.reqCache = make(map[string]int)
	return c
}

func (c *Pinger) setChangedResp(devIp string, status int) {
	if dev, exist := c.respChangedCache[devIp]; exist {
		c.respChangedCache[devIp] = dev + status
	} else {
		c.respChangedCache[devIp] = status
	}
}
func (c *Pinger) deleteDevFromRequest(devIp string) {
	delete(c.reqCache, devIp)
}

func (c *Pinger) setResp(devIp string) {
	c.Lock()
	defer c.Unlock()
	if dev, exist := c.respCache[devIp]; exist {
		c.respCache[devIp] = dev + 1
	} else {
		c.respCache[devIp] = 1
	}
}

func (c *Pinger) setTCPResp(devIp string) {
	c.Lock()
	defer c.Unlock()
	c.respCache[devIp] = 999
}

func (c *Pinger) getChanges() map[string]int {
	return c.respChangedCache
}

func (c *Pinger) getResponses() map[string]int {
	return c.respCache
}

func (c *Pinger) reopenSockets() error {
	c.lg.DebugF("Start reopening sockets...")
	var err error
	c.Lock()
	defer c.Unlock()
	if c.icmpSocket != nil {
		c.lg.DebugF("ICMP socket is open, closing...")
		c.icmpSocket.Close()
	}
	if c.icmp6Socket != nil && c.Config.ICMP.EnableIPv6 {
		c.lg.DebugF("ICMP6 socket is open, closing...")
		c.icmp6Socket.Close()
	}
	//Open ICMP socket
	c.icmpSocket, err = icmp.ListenPacket("ip4:icmp", "")
	if err != nil {
		return tracerr.Wrap(err)
	} else {
		c.lg.DebugF("Success opened ICMP socket...")
	}
	//Open ICMP6 socket
	if c.Config.ICMP.EnableIPv6 {
		c.icmp6Socket, err = icmp.ListenPacket("ip6:ipv6-icmp", "")
		if err != nil {
			return tracerr.Wrap(err)
		} else {
			c.lg.DebugF("Success opened ICMP6 socket...")
		}
	}
	return nil
}

func (c *Pinger) setTimeStart(peer string) {
	c.Lock()
	defer c.Unlock()
	data := c.icmpTiming[peer]
	data.Start = time.Now().UnixNano()
	c.icmpTiming[peer] = data
}
func (c *Pinger) setTimeStop(peer string) {
	c.Lock()
	defer c.Unlock()

	data := c.icmpTiming[peer]
	data.Stop = time.Now().UnixNano()
	c.icmpTiming[peer] = data
}

func (c *Pinger) getTimings() map[string]int64 {
	c.Lock()
	defer c.Unlock()
	r := make(map[string]int64)
	for ip, t := range c.icmpTiming {
		if t.Start != 0 && t.Stop != 0 && t.Start <= t.Stop {
			//Request time and restonse time setted
			r[ip] = (t.Stop - t.Start)
		} else if t.Start != 0 && t.Stop == 0 {
			r[ip] = -1
		} else {
			c.lg.Noticef("Timing for %v corrupted. StartTime=%v, StopTime=%v", ip, t.Start, t.Stop)
		}
	}
	return r
}

func (c *Pinger) clearTimings() {
	c.Lock()
	defer c.Unlock()
	for ip, _ := range c.icmpTiming {
		c.icmpTiming[ip] = IcmpTiming{
			Start: 0,
			Stop:  0,
		}
	}
}
