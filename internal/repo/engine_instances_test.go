package repo

import (
	"testing"
	"time"
)

func TestEngineInstanceHeartbeatLookupAndExpiry(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if got := s.EngineInstanceControlURL("worker-a"); got != "" {
		t.Fatalf("empty registry URL = %q", got)
	}
	if err := s.HeartbeatEngineInstance(EngineInstance{InstanceID: "worker-a", Role: "worker", ControlURL: "http://worker-a:8081", WorkspaceHost: "worker-a"}, 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatEngineInstance(EngineInstance{InstanceID: "worker-b", Role: "worker", ControlURL: "http://worker-b:8081", WorkspaceHost: "worker-b"}, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := s.EngineInstanceControlURL("worker-a"); got != "http://worker-a:8081" {
		t.Fatalf("live URL = %q", got)
	}
	urls := s.LiveWorkerControlURLs()
	if len(urls) != 2 {
		t.Fatalf("live workers = %v", urls)
	}
	time.Sleep(60 * time.Millisecond)
	if got := s.EngineInstanceControlURL("worker-a"); got != "" {
		t.Fatalf("expired URL = %q", got)
	}
	if got := s.EngineInstanceControlURL("worker-b"); got != "http://worker-b:8081" {
		t.Fatalf("still-live URL = %q", got)
	}
	if err := s.DeleteEngineInstance("worker-b"); err != nil {
		t.Fatal(err)
	}
	if got := s.EngineInstanceControlURL("worker-b"); got != "" {
		t.Fatalf("deleted URL = %q", got)
	}
}
