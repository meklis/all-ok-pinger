package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meklis/all-ok-pinger/state"
	"golang.org/x/net/websocket"
)

// Websocket messages (JSON, one per frame):
//
//	{"type":"status_changed|falling|rising|falling_cancelled|rising_cancelled|host_added|host_removed", "ident":.., ...event}
//	  - the same as events in redis
//	{"type":"inspection","time":..,"progress":{..},"pending":[{ip,status,state,failed_inspections,...}]}
//	  - after every inspection: all hosts which are falling / rising now
//	{"type":"cycle","time":..,"cycle":{..}} - cycle finished, statuses are fixed
//	{"type":"ping","time":..} - keepalive
//
// Filters (query): types=falling,rising,cycle - message types; ip=10.0.0.1,10.0.0.2 - events of hosts.
const (
	wsClientBuffer = 1024
	wsPingInterval = 30 * time.Second
	wsWriteTimeout = 10 * time.Second
)

type wsMessage struct {
	typ  string
	ip   string
	data interface{}
}

type wsClient struct {
	out   chan []byte
	types map[string]bool
	ips   map[string]bool
}

func (c *wsClient) accepts(m wsMessage) bool {
	if m.typ == "ping" {
		return true
	}
	if len(c.types) > 0 && !c.types[m.typ] {
		return false
	}
	return m.ip == "" || len(c.ips) == 0 || c.ips[m.ip]
}

type hub struct {
	ident   string
	in      chan wsMessage
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

func newHub(ident string) *hub {
	h := &hub{ident: ident, in: make(chan wsMessage, 10000), clients: map[*wsClient]struct{}{}}
	go h.run()
	go func() {
		for range time.Tick(wsPingInterval) {
			h.publish(wsMessage{typ: "ping", data: map[string]interface{}{"type": "ping", "time": time.Now()}})
		}
	}()
	return h
}

// publish - never blocks, message is dropped if queue is full
func (h *hub) publish(m wsMessage) {
	select {
	case h.in <- m:
	default:
	}
}

func (h *hub) run() {
	for m := range h.in {
		h.mu.Lock()
		if len(h.clients) == 0 {
			h.mu.Unlock()
			continue
		}
		data, err := json.Marshal(m.data)
		if err != nil {
			h.mu.Unlock()
			continue
		}
		for c := range h.clients {
			if !c.accepts(m) {
				continue
			}
			select {
			case c.out <- data:
			default:
				// slow client - disconnect, it will reconnect and reload state
				delete(h.clients, c)
				close(c.out)
			}
		}
		h.mu.Unlock()
	}
}

type wsEvent struct {
	Ident string `json:"ident"`
	state.Event
}

// recentEvents - ring buffer of last events, for /api/v1/events (dashboard log after opening)
type recentEvents struct {
	mu   sync.Mutex
	list []wsEvent
	next int
	full bool
}

const recentEventsSize = 2000

func (r *recentEvents) add(e wsEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.list == nil {
		r.list = make([]wsEvent, recentEventsSize)
	}
	r.list[r.next] = e
	r.next = (r.next + 1) % recentEventsSize
	if r.next == 0 {
		r.full = true
	}
}

// newest - last events, newest first
func (r *recentEvents) newest(types map[string]bool, limit int) []wsEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = recentEventsSize
	}
	res := make([]wsEvent, 0, limit)
	for i := 0; i < n && len(res) < limit; i++ {
		e := r.list[(r.next-1-i+recentEventsSize)%recentEventsSize]
		if len(types) == 0 || types[e.Type] {
			res = append(res, e)
		}
	}
	return res
}

// Event - host event from store
func (s *Server) Event(e state.Event) {
	m := wsEvent{Ident: s.hub.ident, Event: e}
	s.recent.add(m)
	s.hub.publish(wsMessage{typ: e.Type, ip: e.Ip, data: m})
}

// GET /api/v1/events?types=status_changed,falling&limit=500 - last events (max 2000), newest first
func (s *Server) eventsHandler(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > recentEventsSize {
		limit = recentEventsSize
	}
	data := s.recent.newest(splitSet(r.URL.Query().Get("types")), limit)
	writeJSON(w, http.StatusOK, map[string]interface{}{"statusCode": 200, "meta": map[string]int{"count": len(data)}, "data": data})
}

// Inspection - inspection finished: progress and hosts in transition
func (s *Server) Inspection(progress state.Progress, pending []state.PendingHost) {
	s.hub.publish(wsMessage{typ: "inspection", data: map[string]interface{}{
		"type": "inspection", "time": time.Now(), "progress": progress, "pending": pending,
	}})
}

// Cycle - cycle finished
func (s *Server) Cycle(info state.CycleInfo) {
	s.hub.publish(wsMessage{typ: "cycle", data: map[string]interface{}{"type": "cycle", "time": time.Now(), "cycle": info}})
}

func splitSet(v string) map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			m[s] = true
		}
	}
	return m
}

// GET /api/v1/ws
func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c := &wsClient{out: make(chan []byte, wsClientBuffer), types: splitSet(q.Get("types")), ips: splitSet(q.Get("ip"))}
	websocket.Server{Handler: func(conn *websocket.Conn) {
		defer conn.Close()
		s.hub.mu.Lock()
		s.hub.clients[c] = struct{}{}
		s.hub.mu.Unlock()
		defer func() {
			s.hub.mu.Lock()
			if _, ok := s.hub.clients[c]; ok {
				delete(s.hub.clients, c)
				close(c.out)
			}
			s.hub.mu.Unlock()
		}()
		// reader: detect closing of connection
		closed := make(chan struct{})
		go func() {
			var msg string
			for websocket.Message.Receive(conn, &msg) == nil {
			}
			close(closed)
		}()
		for {
			select {
			case data, ok := <-c.out:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
				if websocket.Message.Send(conn, string(data)) != nil {
					return
				}
			case <-closed:
				return
			}
		}
	}}.ServeHTTP(w, r)
}
