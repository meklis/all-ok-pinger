package pinger

import (
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	ICMP_RESPONSE_BUFFER_BYTES = 1500
	ProtocolICMP               = 1
	ProtocolIPv6ICMP           = 58

	// payload: [4 bytes cycle id][8 bytes unix nano send time]
	payloadLen = 12
)

func (c *Pinger) getSocket(v6 bool) *net.IPConn {
	c.sockMu.RLock()
	defer c.sockMu.RUnlock()
	if v6 {
		return c.icmp6Socket
	}
	return c.icmpSocket
}

// sendJob - one echo request to host
type sendJob struct {
	h     *hostCycle
	seq   int
	cycle uint32
}

// writeSocket - sends packets from queue with pacing (packets_per_second / writers for every writer).
// Sleep is done in chunks (>= 1ms), so real rate does not depend on timer accuracy.
func (c *Pinger) writeSocket() {
	interval := c.sendInterval
	next := time.Now()
	// own send-only sockets for every writer - sending to one socket is serialized by the kernel
	w := &writer{}
	defer w.close()
	for job := range c.chanReq {
		if interval > 0 {
			now := time.Now()
			if next.Before(now.Add(-10 * time.Millisecond)) {
				// writer was idle or can't keep the rate - do not burst
				next = now
			}
			if d := next.Sub(now); d >= time.Millisecond {
				time.Sleep(d)
			}
			next = next.Add(interval)
		}
		c.sendPacket(w, job)
		c.sendWg.Done()
	}
}

type writer struct {
	v4, v6 *net.IPConn
}

func (w *writer) close() {
	if w.v4 != nil {
		w.v4.Close()
	}
	if w.v6 != nil {
		w.v6.Close()
	}
}

// openSendSocket - raw ICMP socket for sending only: all incoming ICMP is filtered by the kernel
func openSendSocket(v6 bool) (*net.IPConn, error) {
	if v6 {
		conn, err := net.ListenIP("ip6:ipv6-icmp", nil)
		if err != nil {
			return nil, err
		}
		var f ipv6.ICMPFilter
		f.SetAll(true)
		_ = ipv6.NewPacketConn(conn).SetICMPFilter(&f)
		return conn, nil
	}
	conn, err := net.ListenIP("ip4:icmp", nil)
	if err != nil {
		return nil, err
	}
	var f ipv4.ICMPFilter
	f.SetAll(true)
	_ = ipv4.NewPacketConn(conn).SetICMPFilter(&f)
	return conn, nil
}

func (w *writer) socket(v6 bool) (*net.IPConn, error) {
	var err error
	if v6 {
		if w.v6 == nil {
			w.v6, err = openSendSocket(true)
		}
		return w.v6, err
	}
	if w.v4 == nil {
		w.v4, err = openSendSocket(false)
	}
	return w.v4, err
}

func (w *writer) reset(v6 bool) {
	if v6 && w.v6 != nil {
		w.v6.Close()
		w.v6 = nil
	} else if !v6 && w.v4 != nil {
		w.v4.Close()
		w.v4 = nil
	}
}

func (c *Pinger) sendPacket(w *writer, job sendJob) {
	h := job.h
	data := make([]byte, payloadLen)
	binary.BigEndian.PutUint32(data[0:4], job.cycle)
	binary.BigEndian.PutUint64(data[4:12], uint64(time.Now().UnixNano()))
	var msgType icmp.Type = ipv4.ICMPTypeEcho
	if h.ipv6 {
		msgType = ipv6.ICMPTypeEchoRequest
	}
	wm := icmp.Message{
		Type: msgType,
		Code: 0,
		Body: &icmp.Echo{
			ID:   c.icmpID,
			Seq:  job.seq & 0xffff,
			Data: data,
		},
	}
	wb, err := wm.Marshal(nil)
	if err != nil {
		c.lg.Errorf("Error generate ICMP package - %v", err.Error())
		return
	}
	sock, err := w.socket(h.ipv6)
	if err != nil {
		c.lg.Errorf("Error open ICMP socket for sending (ipv6=%v): %v", h.ipv6, err)
		time.Sleep(time.Millisecond * 100)
		return
	}
	if _, err := sock.WriteTo(wb, h.addr); err != nil {
		c.lg.Errorf("Problem write to ICMP socket for %v: %v", h.key, err)
		if !isTemporary(err) {
			w.reset(h.ipv6)
		}
		time.Sleep(time.Millisecond * 10)
		return
	}
	atomic.AddInt32(&h.sent, 1)
}

func (c *Pinger) readSocket(v6 bool) {
	rb := make([]byte, ICMP_RESPONSE_BUFFER_BYTES)
	proto := ProtocolICMP
	if v6 {
		proto = ProtocolIPv6ICMP
	}
	for {
		sock := c.getSocket(v6)
		if sock == nil {
			time.Sleep(time.Second)
			continue
		}
		n, peer, err := sock.ReadFrom(rb)
		recvAt := time.Now()
		if err != nil {
			// Socket may be already reopened by another reader
			if c.getSocket(v6) != sock {
				continue
			}
			c.lg.Errorf("Error read from socket: %v", err)
			if err := c.reopenSocket(v6, sock); err != nil {
				c.lg.Errorf("Error reopen sockets: %v", err)
				time.Sleep(time.Second)
			} else {
				c.lg.Infof("Success reopen sockets")
			}
			continue
		}

		rm, err := icmp.ParseMessage(proto, rb[:n])
		if err != nil {
			c.lg.Errorf("Error parse ICMP package: %v", err)
			continue
		}
		switch rm.Type {
		case ipv4.ICMPTypeEchoReply, ipv6.ICMPTypeEchoReply:
		case ipv4.ICMPTypeParameterProblem, ipv6.ICMPTypeParameterProblem:
			c.lg.Infof("ICMP ParameterProblem message from peer %v", peer.String())
			continue
		default:
			continue
		}
		echo, ok := rm.Body.(*icmp.Echo)
		if !ok || echo.ID != c.icmpID || len(echo.Data) < payloadLen {
			// Not our packet
			continue
		}
		cycle := binary.BigEndian.Uint32(echo.Data[0:4])
		sentAt := int64(binary.BigEndian.Uint64(echo.Data[4:12]))
		rtt := time.Duration(recvAt.UnixNano() - sentAt)
		if rtt < 0 {
			continue
		}
		var ip net.IP
		switch p := peer.(type) {
		case *net.IPAddr:
			ip = p.IP
		default:
			ip = net.ParseIP(peer.String())
		}
		if ip == nil {
			continue
		}
		c.handleReply(ip.String(), cycle, uint16(echo.Seq), rtt)
	}
}

func (c *Pinger) handleReply(key string, cycle uint32, seq uint16, rtt time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cycle != c.cycleID {
		return
	}
	h, ok := c.hosts[key]
	if !ok {
		return
	}
	if _, dup := h.seen[seq]; dup {
		return
	}
	h.seen[seq] = struct{}{}
	h.received++
	if h.rttCount == 0 || rtt < h.rttMin {
		h.rttMin = rtt
	}
	if rtt > h.rttMax {
		h.rttMax = rtt
	}
	h.rttSum += rtt
	h.rttCount++
	// Response counted for inspection only if it belongs to current inspection
	if count := c.Config.ICMP.Packets; count > 0 && int(seq)/count == c.inspection {
		h.inspResp++
	}
}

func (c *Pinger) reopenSocket(v6 bool, failed *net.IPConn) error {
	c.sockMu.Lock()
	defer c.sockMu.Unlock()
	if v6 {
		if failed != nil && c.icmp6Socket != failed {
			return nil
		}
		if c.icmp6Socket != nil {
			c.icmp6Socket.Close()
		}
		s, err := net.ListenIP("ip6:ipv6-icmp", nil)
		if err != nil {
			c.icmp6Socket = nil
			return err
		}
		c.setReadBuffer(s)
		c.icmp6Socket = s
		return nil
	}
	if failed != nil && c.icmpSocket != failed {
		return nil
	}
	if c.icmpSocket != nil {
		c.icmpSocket.Close()
	}
	s, err := net.ListenIP("ip4:icmp", nil)
	if err != nil {
		c.icmpSocket = nil
		return err
	}
	c.setReadBuffer(s)
	c.icmpSocket = s
	return nil
}

// isTemporary - errors of sending to unreachable host etc., socket is still usable
func isTemporary(err error) bool {
	for _, e := range []error{syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.ENOBUFS, syscall.EPERM, syscall.EACCES, syscall.EINVAL} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// setReadBuffer - set receive buffer of socket. SO_RCVBUFFORCE (root) allows to exceed net.core.rmem_max.
// Small buffer leads to drops of replies on big amount of hosts.
func (c *Pinger) setReadBuffer(conn *net.IPConn) {
	size := socketReadBuffer
	forced := false
	if raw, err := conn.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			forced = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUFFORCE, size) == nil
		})
	}
	if !forced {
		if err := conn.SetReadBuffer(size); err != nil {
			c.lg.Warnf("Error set socket read buffer: %v", err)
		} else {
			c.lg.Warnf("SO_RCVBUFFORCE is not permitted, read buffer limited by net.core.rmem_max")
		}
	}
}
