package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meklis/all-ok-pinger/state"
	"golang.org/x/net/websocket"
)

func dial(t *testing.T, srv *httptest.Server, query string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws" + query
	conn, err := websocket.Dial(url, "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func receive(t *testing.T, conn *websocket.Conn) map[string]interface{} {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg string
	if err := websocket.Message.Receive(conn, &msg); err != nil {
		t.Fatal(err)
	}
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(msg), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWebsocket(t *testing.T) {
	s := NewServer(state.NewStore(), Info{PingerIdent: "TEST"}, nil)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	all := dial(t, srv, "")
	defer all.Close()
	filtered := dial(t, srv, "?types=falling,cycle&ip=10.0.0.2")
	defer filtered.Close()
	for {
		s.hub.mu.Lock()
		n := len(s.hub.clients)
		s.hub.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	st := 100
	s.Event(state.Event{Type: state.EventFalling, Ip: "10.0.0.1", Status: &st})
	s.Event(state.Event{Type: state.EventStatusChanged, Ip: "10.0.0.2", Status: &st})
	s.Event(state.Event{Type: state.EventFalling, Ip: "10.0.0.2", Status: &st})
	s.Inspection(state.Progress{Inspection: 2}, []state.PendingHost{{Ip: "10.0.0.2", Pending: &state.Pending{State: "falling", FailedInspections: 2}}})
	s.Cycle(state.CycleInfo{Hosts: 2})

	for _, want := range []string{"falling", "status_changed", "falling", "inspection", "cycle"} {
		if m := receive(t, all); m["type"] != want {
			t.Fatalf("all: want %v, got %v", want, m)
		}
	}
	if ev := s.recent.newest(map[string]bool{"falling": true}, 10); len(ev) != 2 || ev[0].Ip != "10.0.0.2" || ev[1].Ip != "10.0.0.1" {
		t.Fatalf("bad recent events %+v", ev)
	}
	m := receive(t, filtered)
	if m["type"] != "falling" || m["ip"] != "10.0.0.2" || m["ident"] != "TEST" {
		t.Fatalf("filtered: bad event %v", m)
	}
	if m := receive(t, filtered); m["type"] != "cycle" {
		t.Fatalf("filtered: want cycle, got %v", m)
	}
}
