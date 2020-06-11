package pinger

import (
	"github.com/meklis/all-ok-pinger/prom"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"math/rand"
	"net"
	"os"
	"strings"
	"time"
)

const (
	ICMP_RESPONSE_BUFFER_BYTES = 1024
	ProtocolICMP               = 1
	ProtocolIPv6ICMP           = 58
)

func (c *Pinger) writeSocket() {
	for {
		dev := <-c.chanReq
		if strings.Contains(dev.Ip, ".") {
			for i := 0; i < c.Config.ICMP.CountPackagesToHost; i++ {
				//Создадим ICMP пакет
				wm := icmp.Message{
					Type: ipv4.ICMPTypeEcho,
					Code: 0,
					Body: &icmp.Echo{
						ID:   os.Getpid() & 0xffff,
						Seq:  i,
						Data: []byte(dev.Ip),
					},
				}

				//Переведем ICMP пакет в байты
				wb, err := wm.Marshal(nil)
				if err != nil {
					c.lg.Errorf("Error generate ICMP package - %v", err.Error())
				}

				//Закидываем пакет в сокет
				prom.CountPingPackagesInc(dev.Ip)
				c.setTimeStart(dev.Ip)
				if _, err := c.icmpSocket.WriteTo(wb, &net.IPAddr{IP: net.ParseIP(dev.Ip)}); err != nil {
					c.lg.Errorf("Problem write to ICMP socket: %v", err)
					time.Sleep(time.Millisecond * 10)
				}
				time.Sleep(c.Config.ICMP.TimeDurationToSend)
			}
		} else {
			for i := 0; i < c.Config.ICMP.CountPackagesToHost; i++ {
				//Создадим ICMP пакет
				wm := icmp.Message{
					Type: ipv6.ICMPTypeEchoRequest, Code: 0,
					Body: &icmp.Echo{
						ID: rand.Intn(65535), Seq: i,
						Data: []byte(dev.Ip),
					},
				}

				//Переведем ICMP пакет в байты
				wb, err := wm.Marshal(nil)
				if err != nil {
					c.lg.Errorf("Error generate ICMP6 package - %v", err.Error())
				}

				//Закидываем пакет в сокет
				prom.CountPingPackagesInc(dev.Ip)
				if _, err := c.icmp6Socket.WriteTo(wb, &net.IPAddr{IP: net.ParseIP(dev.Ip)}); err != nil {
					c.lg.Errorf("Problem write to ICMP6 socket: %v", err)
					time.Sleep(time.Millisecond * 10)
				}
				time.Sleep(c.Config.ICMP.TimeDurationToSend)
			}
		}
	}
}
func (c *Pinger) readSocket() {
	rb := make([]byte, ICMP_RESPONSE_BUFFER_BYTES)
	for {
		n, peer, err := c.icmpSocket.ReadFrom(rb)

		if err != nil {
			c.lg.Errorf("Error read from socket: %v", err)
			if err := c.reopenSockets(); err != nil {
				c.lg.CriticalF("Error reopen sockets: ", err)
			} else {
				c.lg.InfoF("Success reopen sockets")
			}
			continue
		}

		rm, err := icmp.ParseMessage(ProtocolICMP, rb[:n])
		if err != nil {
			c.lg.Errorf("Error parse ICMP package: %v", err)
			continue
		}

		switch rm.Type {
		case ipv4.ICMPTypeEchoReply:
			prom.CountPongPackagesInc(peer.String())
			c.setTimeStop(peer.String())
			c.setResp(peer.String())
		case ipv4.ICMPTypeParameterProblem:
			c.lg.InfoF("ICMPParameterProblem message from peer %v", peer.String())
		default:
		}
	}
}
func (c *Pinger) readSocket6() {
	rb := make([]byte, ICMP_RESPONSE_BUFFER_BYTES)
	for {
		n, peer, err := c.icmp6Socket.ReadFrom(rb)

		//Если при чтении возни
		if err != nil {
			c.lg.Errorf("Error read from socket: %v", err)
			if err := c.reopenSockets(); err != nil {
				c.lg.CriticalF("Error reopen sockets: ", err)
			} else {
				c.lg.InfoF("Success reopen sockets")
			}
			continue
		}

		//Парсим прочитаный пакет
		rm, err := icmp.ParseMessage(ProtocolIPv6ICMP, rb[:n])
		if err != nil {
			c.lg.Errorf("Error parse ICMP package: %v", err)
			continue
		}
		//p(rm.Type)
		//Обрабатываем результат
		switch rm.Type {
		case ipv6.ICMPTypeEchoReply:
			prom.CountPongPackagesInc(peer.String())
			c.setTimeStop(peer.String())
			c.setResp(peer.String())
		case ipv6.ICMPTypeParameterProblem:
			c.lg.InfoF("ICMP6ParameterProblem message from peer %v", peer.String())
		default:
		}

	}
}
