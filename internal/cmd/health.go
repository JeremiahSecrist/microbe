package cmd

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"microbe/internal/config"
)

// waitHealthy is a seam over TCP dialing so tests can fake it without a real
// listener. The default dials addr every interval until it accepts a
// connection or budget elapses, returning whether it became reachable.
var waitHealthy = func(addr string, dialTimeout, interval, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err == nil {
			conn.Close()
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// probeHealth polls hc's port on ip until healthy or its StartPeriod budget
// elapses. Durations are assumed already validated by Compose.Validate.
func probeHealth(hc config.Healthcheck, ip string) (healthy bool, err error) {
	interval, err := time.ParseDuration(hc.Interval)
	if err != nil {
		return false, fmt.Errorf("healthcheck: invalid interval %q: %w", hc.Interval, err)
	}
	timeout, err := time.ParseDuration(hc.Timeout)
	if err != nil {
		return false, fmt.Errorf("healthcheck: invalid timeout %q: %w", hc.Timeout, err)
	}
	startPeriod, err := time.ParseDuration(hc.StartPeriod)
	if err != nil {
		return false, fmt.Errorf("healthcheck: invalid startPeriod %q: %w", hc.StartPeriod, err)
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(hc.Port))
	return waitHealthy(addr, timeout, interval, startPeriod), nil
}
