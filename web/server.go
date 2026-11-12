// Package web - HTTP server with API of current hosts state, prometheus metrics and dashboard.
package web

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/meklis/all-ok-pinger/pinger"
	"github.com/meklis/all-ok-pinger/state"
)

//go:embed static
var staticFiles embed.FS

type Info struct {
	Version     string `json:"version"`
	BuildDate   string `json:"build_date"`
	PingerIdent string `json:"pinger_ident"`
}

type Server struct {
	store  *state.Store
	info   Info
	config interface{}
	mux    *http.ServeMux
	hub    *hub
	recent recentEvents
}

// NewServer - config is any (sanitized) structure, it is returned by /api/v1/config as is
func NewServer(store *state.Store, info Info, config interface{}) *Server {
	s := &Server{store: store, info: info, config: config, mux: http.NewServeMux(), hub: newHub(info.PingerIdent)}
	static, _ := fs.Sub(staticFiles, "static")
	s.mux.Handle("GET /", http.FileServer(http.FS(static)))
	s.mux.HandleFunc("GET /api/v1/hosts", withGzip(s.hosts))
	s.mux.HandleFunc("GET /api/v1/hosts/{ip}", s.host)
	s.mux.HandleFunc("GET /api/v1/summary", withGzip(s.summary))
	s.mux.HandleFunc("GET /api/v1/ws", s.ws)
	s.mux.HandleFunc("GET /api/v1/events", withGzip(s.eventsHandler))
	s.mux.HandleFunc("GET /api/v1/config", s.configHandler)
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return s
}

// EnablePprof - register /debug/pprof/ handlers
func (s *Server) EnablePprof() {
	s.mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	s.mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	s.mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	s.mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	s.mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
}

// Handle - register additional handler (prometheus metrics for example)
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

type gzipWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w gzipWriter) Write(b []byte) (int, error) { return w.gz.Write(b) }

// withGzip - compress response if client supports it (host list of 50k hosts is ~50MB of JSON)
func withGzip(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			f(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer gz.Close()
		f(gzipWriter{ResponseWriter: w, gz: gz}, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(data)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]interface{}{"statusCode": code, "error": msg})
}

// GET /api/v1/hosts?status=up|down|unknown&state=up,down,unknown,falling,rising&target_group=...&ip=1.1.1.1,2.2.2.2&q=search
func (s *Server) hosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	statusFilter := q.Get("status")
	states := map[string]bool{}
	for _, st := range strings.Split(q.Get("state"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			states[st] = true
		}
	}
	group := q.Get("target_group")
	search := strings.ToLower(q.Get("q"))
	ips := map[string]bool{}
	for _, ip := range strings.Split(q.Get("ip"), ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			ips[ip] = true
		}
	}
	list := s.store.List()
	data := make([]map[string]interface{}, 0, len(list))
	for i := range list {
		h := &list[i]
		if len(ips) > 0 && !ips[h.Ip] {
			continue
		}
		if group != "" && h.TargetGroup != group {
			continue
		}
		switch statusFilter {
		case "up":
			if !h.IsUp() {
				continue
			}
		case "down":
			if h.Status == nil || *h.Status > 0 {
				continue
			}
		case "unknown":
			if h.Status != nil {
				continue
			}
		}
		if len(states) > 0 && !states[h.State()] {
			continue
		}
		if search != "" && !matches(h, search) {
			continue
		}
		data = append(data, h.ApiObject())
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"statusCode": 200,
		"meta":       map[string]interface{}{"count": len(data)},
		"data":       data,
	})
}

func matches(h *state.HostState, search string) bool {
	if strings.Contains(strings.ToLower(h.Ip), search) || strings.Contains(strings.ToLower(h.TargetGroup), search) {
		return true
	}
	for _, v := range h.Attributes {
		if strings.Contains(strings.ToLower(state.AttributeValue(v)), search) {
			return true
		}
	}
	return false
}

// GET /api/v1/hosts/{ip}
func (s *Server) host(w http.ResponseWriter, r *http.Request) {
	ip := r.PathValue("ip")
	h, ok := s.store.Get(ip)
	if !ok {
		// try normalized form of IP address
		if parsed := net.ParseIP(ip); parsed != nil {
			for _, item := range s.store.List() {
				if p := net.ParseIP(item.Ip); p != nil && p.Equal(parsed) {
					h, ok = item, true
					break
				}
			}
		}
	}
	if !ok {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"statusCode": 200, "data": h.ApiObject()})
}

type summaryHosts struct {
	Total    int `json:"total"`
	Falling  int `json:"falling"`
	Rising   int `json:"rising"`
	Up       int `json:"up"`
	Down     int `json:"down"`
	Unknown  int `json:"unknown"`
	TcpOnly  int `json:"tcp_only"`
	WithLoss int `json:"with_loss"`
	Errors   int `json:"errors"`
	// ReportPending - hosts waiting for report (status differs from status known by server)
	ReportPending int `json:"report_pending"`
}

// GET /api/v1/summary
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	list := s.store.List()
	hs := summaryHosts{Total: len(list)}
	groups := map[string]*summaryHosts{}
	var latSum float64
	var latCount int
	var sent, recv int
	for i := range list {
		h := &list[i]
		g := groups[h.TargetGroup]
		if g == nil {
			g = &summaryHosts{}
			groups[h.TargetGroup] = g
		}
		g.Total++
		for _, c := range []*summaryHosts{&hs, g} {
			switch {
			case h.Error != "":
				c.Errors++
			}
			if h.ReportPending {
				c.ReportPending++
			}
			switch {
			case h.Status == nil:
				c.Unknown++
			case *h.Status > 0:
				c.Up++
				if *h.Status == pinger.StatusTcpUp {
					c.TcpOnly++
				}
			default:
				c.Down++
			}
			if h.IsUp() && h.Loss.Loss > 0 {
				c.WithLoss++
			}
			switch h.State() {
			case state.StateFalling:
				c.Falling++
			case state.StateRising:
				c.Rising++
			}
		}
		if h.Latency != nil {
			latSum += h.Latency.Avg
			latCount++
		}
		sent += h.Loss.Transmitted
		recv += h.Loss.Received
	}
	cycle, cycles, startedAt := s.store.CycleInfo()
	resp := map[string]interface{}{
		"info":       s.info,
		"started_at": startedAt.Format(time.RFC3339),
		"uptime_sec": int64(time.Since(startedAt).Seconds()),
		"cycles":     cycles,
		"last_cycle": cycle,
		"progress":   s.store.Progress(),
		"host_list":  s.store.HostListInfo(),
		"hosts":      hs,
		"groups":     groups,
		"packets":    map[string]int{"transmitted": sent, "received": recv},
	}
	if latCount > 0 {
		resp["avg_latency_ms"] = float64(int(latSum/float64(latCount)*1000)) / 1000
	} else {
		resp["avg_latency_ms"] = nil
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /api/v1/config
func (s *Server) configHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.config)
}
