package events

import (
	"strings"
	"testing"
)

func TestDetectURL(t *testing.T) {
	u, err := DetectURL("127.0.0.1:6379", "10.1.1.1:2112")
	if err != nil || u != "http://10.1.1.1:2112" {
		t.Fatalf("explicit host: %v %v", u, err)
	}
	u, err = DetectURL("10.255.255.1:6379", ":2112")
	if err != nil || !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":2112") || strings.Contains(u, "127.0.0.1") {
		t.Fatalf("detected: %v %v", u, err)
	}
	if _, err := DetectURL("127.0.0.1:6379", "2112"); err == nil {
		t.Fatal("wrong listen must fail")
	}
}
