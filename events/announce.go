package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// Instance - registration of pinger in redis (key <announce_key>:<ident>, TTL announce_ttl).
// Used by aggregator for discovery of pingers.
type Instance struct {
	Ident     string    `json:"ident"`
	URL       string    `json:"url"`
	Hostname  string    `json:"hostname"`
	Version   string    `json:"version"`
	BuildDate string    `json:"build_date"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StartAnnounce - register pinger in redis every announce_interval until ctx is done.
// Key is removed on stop, otherwise it expires after announce_ttl.
func (p *Publisher) StartAnnounce(ctx context.Context, inst Instance) {
	if p.conf.AnnounceKey == "" {
		return
	}
	interval, ttl := p.conf.AnnounceInterval, p.conf.AnnounceTTL
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if ttl < interval*2 {
		ttl = interval * 3
	}
	key := p.conf.AnnounceKey + ":" + p.ident
	inst.Ident = p.ident
	if inst.Hostname == "" {
		inst.Hostname, _ = os.Hostname()
	}
	p.lg.Infof("Announcing pinger in redis: key=%v, url=%v, interval=%v", key, inst.URL, interval)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		failed := false
		for {
			inst.UpdatedAt = time.Now()
			data, _ := json.Marshal(inst)
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := p.client.Set(sctx, key, data, ttl).Err()
			cancel()
			if err != nil && !failed {
				p.lg.Warnf("Error announce pinger in redis: %v", err)
			} else if err == nil && failed {
				p.lg.Infof("Pinger announced in redis again")
			}
			failed = err != nil
			select {
			case <-ctx.Done():
				dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				p.client.Del(dctx, key)
				cancel()
				return
			case <-t.C:
			}
		}
	}()
}

// DetectURL - URL of web API of pinger for aggregator: http://<ip>:<port>, where ip is local address
// used to reach redis (in host network - address of node) and port is taken from web.listen.
func DetectURL(redisAddr, listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("wrong web.listen %q: %w", listen, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host, err = localIPFor(redisAddr)
		if err != nil {
			return "", err
		}
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func localIPFor(addr string) (string, error) {
	// UDP "dial" sends nothing, only selects local address by routing table
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return "", fmt.Errorf("detect local address for %v: %w", addr, err)
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP
	if ip.IsLoopback() {
		// redis on the same node through 127.0.0.1 - take first non loopback address
		if ifIP := firstNonLoopback(); ifIP != "" {
			return ifIP, nil
		}
		return "", fmt.Errorf("only loopback address found, set redis.public_url")
	}
	return ip.String(), nil
}

func firstNonLoopback() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.To4() == nil || n.IP.IsLinkLocalUnicast() {
			continue
		}
		// skip docker bridges
		if n.IP.To4()[0] == 172 && n.IP.To4()[1] >= 16 && n.IP.To4()[1] <= 31 {
			continue
		}
		return n.IP.String()
	}
	return ""
}
