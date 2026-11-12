package pinger

import (
	"encoding/json"
	"testing"
)

func TestDeviceUnmarshal(t *testing.T) {
	var list []Device
	data := `[{"ip":"10.0.0.1","status":100,"target_group":"g","_id":15,"_name":"sw-1","_location":{"city":"Kyiv"},"other":1},
	          {"ip":"10.0.0.2","status":null},{"ip":"10.0.0.3"},{"ip":"10.0.0.4","status":"-100"}]`
	if err := json.Unmarshal([]byte(data), &list); err != nil {
		t.Fatal(err)
	}
	if list[0].Status == nil || *list[0].Status != 100 || list[0].TargetGroup != "g" {
		t.Fatalf("bad device: %+v", list[0])
	}
	if len(list[0].Attributes) != 3 || list[0].Attributes["_name"] != "sw-1" || list[0].Attributes["_id"].(json.Number).String() != "15" {
		t.Fatalf("bad attributes: %+v", list[0].Attributes)
	}
	if list[1].Status != nil || list[2].Status != nil {
		t.Fatal("null/missing status must be nil")
	}
	if *list[3].Status != -100 {
		t.Fatal("string status must be parsed")
	}
	out, _ := json.Marshal(list[0])
	var m map[string]interface{}
	json.Unmarshal(out, &m)
	if m["_name"] != "sw-1" || m["ip"] != "10.0.0.1" {
		t.Fatalf("bad marshal: %s", out)
	}
}
