package cmd

import (
	"net"
	"testing"
	"time"

	"microbe/internal/config"
)

func TestWaitHealthyDialsRealListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if !waitHealthy(ln.Addr().String(), 200*time.Millisecond, 10*time.Millisecond, 500*time.Millisecond) {
		t.Error("waitHealthy = false against a real listener, want true")
	}
}

func TestWaitHealthyTimesOutOnRefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now; dials should be refused

	start := time.Now()
	if waitHealthy(addr, 20*time.Millisecond, 10*time.Millisecond, 30*time.Millisecond) {
		t.Error("waitHealthy = true against a closed port, want false")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waitHealthy took %s, want it to give up near the 30ms budget", elapsed)
	}
}

func TestProbeHealthBuildsAddrAndParsesDurations(t *testing.T) {
	origWaitHealthy := waitHealthy
	defer func() { waitHealthy = origWaitHealthy }()

	var gotAddr string
	var gotTimeout, gotInterval, gotBudget time.Duration
	waitHealthy = func(addr string, dialTimeout, interval, budget time.Duration) bool {
		gotAddr, gotTimeout, gotInterval, gotBudget = addr, dialTimeout, interval, budget
		return true
	}

	hc := config.Healthcheck{Interval: "5s", Timeout: "2s", StartPeriod: "10s", Port: 5432}
	healthy, err := probeHealth(hc, "192.168.51.2")
	if err != nil {
		t.Fatal(err)
	}
	if !healthy {
		t.Error("probeHealth = false, want true")
	}
	if want := "192.168.51.2:5432"; gotAddr != want {
		t.Errorf("addr = %q, want %q", gotAddr, want)
	}
	if gotTimeout != 2*time.Second || gotInterval != 5*time.Second || gotBudget != 10*time.Second {
		t.Errorf("durations = timeout=%s interval=%s budget=%s, want 2s/5s/10s", gotTimeout, gotInterval, gotBudget)
	}
}

func TestProbeHealthInvalidDuration(t *testing.T) {
	hc := config.Healthcheck{Interval: "nah", Timeout: "2s", StartPeriod: "10s", Port: 5432}
	if _, err := probeHealth(hc, "192.168.51.2"); err == nil {
		t.Error("want error for invalid interval, got nil")
	}
}
