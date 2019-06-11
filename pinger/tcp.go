package pinger

import (
	"fmt"
	"net"
)

func (c *Pinger) checkingByTCP() {
	for {
		dev := <-c.chanTcpReq
		for _, port := range c.Config.TCP.Ports {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%v:%v", dev.Ip, port), c.Config.TCP.ConnectionTimeout)
			if err == nil {
				conn.Close()
				c.setResp(dev.Ip)
				break
			} else if conn != nil {
				conn.Close()
			}
		}
	}
}
