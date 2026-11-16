package pinger

import (
	"net"
	"strconv"
)

func (c *Pinger) checkingByTCP() {
	for h := range c.chanTcpReq {
		ok := false
		var lastErr error
		for _, port := range c.Config.TCP.Ports {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(h.key, strconv.Itoa(port)), c.Config.TCP.Timeout)
			if conn != nil {
				conn.Close()
			}
			if err == nil {
				ok = true
				c.lg.Debugf("TCP check %v: port %v accepts connections", h.key, port)
				break
			}
			lastErr = err
		}
		if !ok {
			c.lg.Debugf("TCP check %v: no open ports %v (%v)", h.key, c.Config.TCP.Ports, lastErr)
		}
		c.mu.Lock()
		h.tcpChecked = true
		h.tcpOk = ok
		c.mu.Unlock()
		c.tcpWg.Done()
	}
}
