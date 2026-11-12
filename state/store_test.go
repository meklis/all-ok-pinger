package state

import (
	"testing"
	"time"

	"github.com/meklis/all-ok-pinger/pinger"
)

func ip(v int) *int { return &v }

func TestStoreSyncAndUpdate(t *testing.T) {
	s := NewStore()
	s.SetRealtimeConfig(RealtimeConfig{FallingAfterInspections: 2})
	var events []Event
	s.OnEvent(func(e Event) { events = append(events, e) })

	info := s.SyncHosts([]pinger.Device{
		{Ip: "10.0.0.1", Attributes: map[string]interface{}{"_id": "5"}},
		{Ip: "10.0.0.2", Status: ip(100)},
		{Ip: "10.0.0.2"},
	})
	if info.Added != 2 || info.Hosts != 2 {
		t.Fatalf("bad sync info %+v", info)
	}
	// first status of host with known status from list - status_changed event
	s.Update([]pinger.Result{{Device: pinger.Device{Ip: "10.0.0.2", Status: ip(100)}, Status: ip(0)}})
	if len(events) != 1 || events[0].Type != EventStatusChanged || *events[0].PrevStatus != 100 {
		t.Fatalf("bad first status event %+v", events)
	}
	events = nil
	s.SyncHosts([]pinger.Device{
		{Ip: "10.0.0.1", Attributes: map[string]interface{}{"_id": "5"}},
		{Ip: "10.0.0.2", Status: ip(100)},
	})
	// list status is used, if there is no state; null stays null
	devs := s.Devices()
	if devs[0].Status != nil || devs[1].Status == nil || *devs[1].Status != 0 || !devs[1].StatusFromState {
		t.Fatalf("bad devices %+v", devs)
	}

	s.Update([]pinger.Result{{Device: devs[0], Status: ip(100), ICMP: true, Transmitted: 10, Received: 8,
		RttCount: 8, RttMin: time.Millisecond, RttMax: 3 * time.Millisecond, RttSum: 16 * time.Millisecond}})
	h, _ := s.Get("10.0.0.1")
	if h.Latency == nil || h.Latency.Avg != 2 || h.Loss.Loss != 20 || h.ReportObject()["_id"] != "5" {
		t.Fatalf("bad state %+v %+v", h.Latency, h.Loss)
	}
	if len(events) != 0 {
		t.Fatal("first status must not produce event")
	}
	// known status for checking - status of previous pass
	if d := s.Devices(); d[0].Status == nil || *d[0].Status != 100 || !d[0].StatusFromState {
		t.Fatal("must use status of previous pass")
	}
	// reports are disabled - nothing to report
	if p := s.ReportPending(); len(p) != 0 {
		t.Fatalf("reports disabled, pending %v", p)
	}
	s.SetReporting(true, false)
	if p := s.ReportPending(); len(p) != 2 {
		t.Fatalf("both hosts must be reported (unknown for server / differs from list): %v", p)
	}
	s.MarkReported([]string{"10.0.0.1"}, time.Now())
	if p := s.ReportPending(); len(p) != 1 || p[0] != "10.0.0.2" {
		t.Fatalf("10.0.0.1 is reported: %v", p)
	}
	s.SetReporting(false, false)

	// realtime falling -> cancelled
	s.StartCycle(2, 10)
	s.UpdateInspection(1, []pinger.InspectionState{{Ip: "10.0.0.1", Status: ip(100), Failed: 1}})
	if h, _ := s.Get("10.0.0.1"); h.State() != StateUp {
		t.Fatal("1 failed inspection is not falling")
	}
	s.UpdateInspection(2, []pinger.InspectionState{{Ip: "10.0.0.1", Status: ip(100), Failed: 2}})
	if h, _ := s.Get("10.0.0.1"); h.State() != StateFalling || h.Pending.FailedInspections != 2 {
		t.Fatalf("must be falling: %+v", h.Pending)
	}
	if s.Progress().Falling != 1 {
		t.Fatal("bad progress")
	}
	s.Update([]pinger.Result{{Device: devs[0], Status: ip(100), Transmitted: 10, Received: 5}})
	if h, _ := s.Get("10.0.0.1"); h.State() != StateUp || h.Pending != nil {
		t.Fatal("pending must be cleared")
	}
	if len(events) != 2 || events[0].Type != EventFalling || events[1].Type != EventFallingCancelled {
		t.Fatalf("bad events %+v", events)
	}

	// status changed
	s.Update([]pinger.Result{{Device: devs[0], Status: ip(0), Transmitted: 10, Received: 0}})
	h, _ = s.Get("10.0.0.1")
	last := events[len(events)-1]
	if last.Type != EventStatusChanged || *last.PrevStatus != 100 || *last.Status != 0 || h.Flaps != 1 || h.Latency5m == nil {
		t.Fatalf("bad status change %+v", last)
	}

	// host removed from list
	info = s.SyncHosts([]pinger.Device{{Ip: "10.0.0.2"}, {Ip: "10.0.0.3"}})
	if info.Removed != 1 || info.Added != 1 {
		t.Fatalf("bad sync %+v", info)
	}
	if _, ok := s.Get("10.0.0.1"); ok {
		t.Fatal("host must be removed")
	}
	if e := events[len(events)-2:]; e[0].Type != EventHostAdded || e[0].Ip != "10.0.0.3" || e[1].Type != EventHostRemoved || e[1].Ip != "10.0.0.1" {
		t.Fatalf("must be host_added and host_removed events: %+v", e)
	}
	// results of removed host are ignored
	s.Update([]pinger.Result{{Device: devs[0], Status: ip(100)}})
	if _, ok := s.Get("10.0.0.1"); ok {
		t.Fatal("removed host must not be restored")
	}
}

func TestAttributeLabel(t *testing.T) {
	cases := map[string]string{"_id": "id", "_location-name": "location_name", "_host": "attr_host", "__x": "_x", "___y": "attr_y", "_1a": "_1a"}
	for in, want := range cases {
		if got := AttributeLabel(in); got != want {
			t.Errorf("%v: got %v want %v", in, got, want)
		}
	}
}

// host was falling, then responded with loss: falling is cancelled, percent status is changed
func TestFallingRecoveredWithLoss(t *testing.T) {
	s := NewStore()
	var events []Event
	s.OnEvent(func(e Event) { events = append(events, e) })
	s.SyncHosts([]pinger.Device{{Ip: "10.0.0.1", Status: ip(100)}})
	dev := s.Devices()[0]
	s.StartCycle(1, 10)
	s.UpdateInspection(3, []pinger.InspectionState{{Ip: "10.0.0.1", Status: ip(100), Failed: 3}})
	s.Update([]pinger.Result{{Device: dev, Status: ip(33), ICMP: true, Transmitted: 12, Received: 4}})
	if len(events) != 3 || events[0].Type != EventFalling || events[1].Type != EventFallingCancelled ||
		events[2].Type != EventStatusChanged || *events[2].PrevStatus != 100 || *events[2].Status != 33 {
		t.Fatalf("bad events %+v", events)
	}
	if h, _ := s.Get("10.0.0.1"); h.Flaps != 0 || h.State() != StateUp {
		t.Fatalf("percent change is not a flap: flaps=%v state=%v", h.Flaps, h.State())
	}
}

// status in host list differs from status of pinger - host must be reported
func TestReportPendingByHostList(t *testing.T) {
	s := NewStore()
	s.SetReporting(true, false)
	s.SyncHosts([]pinger.Device{{Ip: "10.0.0.1", Status: ip(100)}, {Ip: "10.0.0.2"}})
	devs := s.Devices()
	if devs[0].Status == nil || *devs[0].Status != 100 || devs[0].StatusFromState {
		t.Fatalf("status from list must be used for host without state: %+v", devs[0])
	}
	s.Update([]pinger.Result{{Device: devs[0], Status: ip(100)}, {Device: devs[1], Status: ip(100)}})
	// 10.0.0.1 - same as in list, 10.0.0.2 - server status is unknown (null)
	if p := s.ReportPending(); len(p) != 1 || p[0] != "10.0.0.2" {
		t.Fatalf("bad pending %v", p)
	}
	s.MarkReported([]string{"10.0.0.2"}, time.Now())
	if p := s.ReportPending(); len(p) != 0 {
		t.Fatalf("nothing to report %v", p)
	}
	// host list (loaded after report) has status 0, but host is up - must be reported
	time.Sleep(time.Millisecond)
	s.SyncHosts([]pinger.Device{{Ip: "10.0.0.1", Status: ip(0)}, {Ip: "10.0.0.2", Status: ip(0)}})
	if p := s.ReportPending(); len(p) != 2 {
		t.Fatalf("hosts with other status in list must be reported: %v", p)
	}
	// known status for checking is still status of pinger
	if d := s.Devices(); *d[0].Status != 100 || *d[1].Status != 100 {
		t.Fatalf("must check by status of pinger: %+v", d)
	}
	s.MarkReported([]string{"10.0.0.1", "10.0.0.2"}, time.Now())
	if p := s.ReportPending(); len(p) != 0 {
		t.Fatalf("reported, old list status must be ignored: %v", p)
	}
	// null in host list - server doesn't send statuses, nothing to report
	time.Sleep(time.Millisecond)
	s.SyncHosts([]pinger.Device{{Ip: "10.0.0.1"}, {Ip: "10.0.0.2"}})
	if p := s.ReportPending(); len(p) != 0 {
		t.Fatalf("null in list must not cause report: %v", p)
	}
	// list statuses are ignored
	s.SyncHosts([]pinger.Device{{Ip: "10.0.0.1", Status: ip(0)}, {Ip: "10.0.0.2", Status: ip(0)}})
	s.SetReporting(true, true)
	if p := s.ReportPending(); len(p) != 0 {
		t.Fatalf("ignored list statuses must not cause report: %v", p)
	}
}

// report failed: host is checked by status of pinger (no falling of host, which is already down), and stays pending
func TestReportFailed(t *testing.T) {
	s := NewStore()
	s.SetReporting(true, false)
	var events []Event
	s.OnEvent(func(e Event) { events = append(events, e) })
	s.SyncHosts([]pinger.Device{{Ip: "10.0.0.1", Status: ip(100)}})
	s.Update([]pinger.Result{{Device: s.Devices()[0], Status: ip(0)}})
	// report is not sent; next cycle
	d := s.Devices()[0]
	if *d.Status != 0 {
		t.Fatalf("host must be checked as down: %+v", d)
	}
	s.Update([]pinger.Result{{Device: d, Status: ip(0)}})
	if p := s.ReportPending(); len(p) != 1 {
		t.Fatalf("host must stay pending: %v", p)
	}
	if len(events) != 1 || events[0].Type != EventStatusChanged {
		t.Fatalf("only one status_changed expected: %+v", events)
	}
	if h, _ := s.Get("10.0.0.1"); !h.ReportPending || h.ApiObject()["report_pending"] != true {
		t.Fatal("report_pending must be in API")
	}
}
