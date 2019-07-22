package pinger

import (
	"bitbucket.org/meklis/helpprovider_snmp/logger"
	"github.com/ztrue/tracerr"
	"golang.org/x/net/icmp"
	"sync"
	"time"
)

func (c *Pinger) StartPing(data []Device) (resp []Device) {
	c.lg.DebugF("Запустили пинговалку IPv4/IPv6...")

	//Clear old data, if exists
	c.clearReq().clearChanges().clearResp()

	//Формируем хеш запроса
	for _, dev := range data {
		c.reqCache[dev.Ip] = dev.Status
	}
	for inspect := 0; inspect < c.Config.ICMP.NumberOfInspection; inspect++ {
		c.lg.DebugF("Start inspection number %v. Now %v hosts for ping in list ", inspect+1, len(c.reqCache))
		//Push hosts for ping
		c.lg.DebugF("Start sending requests to workers")
		for ip, status := range c.reqCache {
			c.chanReq <- Device{
				Ip:     ip,
				Status: status,
			}
		}
		c.lg.DebugF("Finished sending requests, waiting responses timeout...")
		//Wait for response
		time.Sleep(c.Config.ICMP.ResponseTimeout)

		//Analize responses
		c.lg.DebugF("Start analize responses...")
		responses := c.getResponses()
		c.lg.Noticef("Received %v responses from switches", len(responses))
		for ip, status := range c.reqCache {
			countResp := responses[ip]
			if countResp == 0 && status == 0 {
				//Как лежал, так и лежит
				c.deleteDevFromRequest(ip)
			} else if status <= 0 && countResp >= c.Config.ICMP.MustPackagesForUp {
				//Лежал и поднимается
				c.setChangedResp(ip, 1)
				c.lg.InfoF("Host %v has status DOWN and now send response in inspection %v, uping...", ip, inspect+1)
			} else if countResp == 0 && status > 0 {
				//Работал, но падает
				c.setChangedResp(ip, -1)
				c.lg.InfoF("Host %v has status UP and now not send response in inspection %v, downing...", ip, inspect+1)
			} else if countResp > 0 && status > 0 {
				//Как работал, так и работает
				c.deleteDevFromRequest(ip)
			}
		}
		c.lg.DebugF("Finished analize responses...")
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
				forUp[ip] = inspections
			} else {
				c.lg.InfoF("Host %v has %v success inspections, must have %v for UP, ignoring...", ip, inspections, c.Config.ICMP.MustInspectionsSuccessForUp)
			}
		} else {
			if absInt(inspections) >= c.Config.ICMP.MustInspectionsFailedForDown {
				forDown[ip] = inspections
			} else {
				c.lg.InfoF("Host %v has %v failed inspections, must have %v for DOWN, ignoring...", ip, absInt(inspections), c.Config.ICMP.MustInspectionsFailedForDown)
			}
		}
	}
	c.lg.Noticef("UP Hosts = %v, DOWN Hosts = %v, Changes summary = %v", len(forUp), len(forDown), len(c.getChanges()))
	if c.Config.TCP.Enable {
		c.lg.DebugF("TCP checking for downing hosts enabled, start checking...")
		for ip, inspections := range forDown {
			c.chanTcpReq <- Device{
				Ip:     ip,
				Status: int((inspections / c.Config.ICMP.NumberOfInspection) * 100),
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
			delete(forDown, host)
		}
	} else {
		c.lg.DebugF("TCP checking is disabled, formate ping result")
	}
	resp = make([]Device, 0, len(forUp)+len(forDown))
	for host, inspections := range forUp {
		resp = append(resp, Device{
			Ip:     host,
			Status: inspections,
		})
	}
	for host, inspections := range forDown {
		resp = append(resp, Device{
			Ip:     host,
			Status: inspections,
		})
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
	pinger.lg = lg
	pinger.reqCache = make(map[string]int, conf.ApproximateHostQuantity)
	pinger.respCache = make(map[string]int, conf.ApproximateHostQuantity)
	pinger.respChangedCache = make(map[string]int, conf.ApproximateHostQuantity)
	pinger.lock = &sync.Mutex{}

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
	c.lock.Lock()
	defer c.lock.Unlock()
	c.respCache = make(map[string]int, c.Config.ApproximateHostQuantity)
	return c
}
func (c *Pinger) clearChanges() *Pinger {
	c.lg.InfoF("Clearing response data...")
	c.lock.Lock()
	defer c.lock.Unlock()
	c.respChangedCache = make(map[string]int, c.Config.ApproximateHostQuantity)
	return c
}

func (c *Pinger) clearReq() *Pinger {
	c.lg.InfoF("Clearing request data...")
	c.lock.Lock()
	defer c.lock.Unlock()
	c.reqCache = make(map[string]int)
	return c
}

func (c *Pinger) setChangedResp(devIp string, status int) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if dev, exist := c.respChangedCache[devIp]; exist {
		c.respChangedCache[devIp] = dev + status
	} else {
		c.respChangedCache[devIp] = status
	}
}
func (c *Pinger) deleteDevFromRequest(devIp string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.reqCache, devIp)
}

func (c *Pinger) setResp(devIp string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if dev, exist := c.respCache[devIp]; exist {
		c.respCache[devIp] = dev + 1
	} else {
		c.respCache[devIp] = 1
	}
}

func (c *Pinger) setTCPResp(devIp string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.respCache[devIp] = 999
}

func (c *Pinger) getChanges() map[string]int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.respChangedCache
}

func (c *Pinger) getResponses() map[string]int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.respCache
}

func (c *Pinger) reopenSockets() error {
	c.lg.DebugF("Start reopening sockets...")
	var err error
	c.lock.Lock()
	defer c.lock.Unlock()
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
