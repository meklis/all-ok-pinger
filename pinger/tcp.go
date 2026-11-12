package pinger

import (
	"net"
	"strconv"
)

func (c *Pinger) checkingByTCP() {
	for h := range c.chanTcpReq {
		ok := false
		for _, port := range c.Config.TCP.Ports {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(h.key, strconv.Itoa(port)), c.Config.TCP.Timeout)
			if conn != nil {
				conn.Close()
			}
			if err == nil {
				ok = true
				break
			}
		}
		c.mu.Lock()
		h.tcpChecked = true
		h.tcpOk = ok
		c.mu.Unlock()
		c.tcpWg.Done()
	}
}
