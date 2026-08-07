package config

import "testing"

func TestApplyDefaultsHealthcheck(t *testing.T) {
	data := []byte(`{
		"name": "test-net",
		"networks": { "backend": { "subnet": "192.168.51.0/24" } },
		"services": {
			"db": {
				"networks": [ { "name": "backend" } ],
				"healthcheck": { "port": 5432 }
			}
		}
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	hc := c.Services["db"].Healthcheck
	if hc == nil {
		t.Fatal("healthcheck missing after Parse")
	}
	if hc.Interval != "5s" {
		t.Errorf("Interval = %q, want 5s", hc.Interval)
	}
	if hc.Timeout != "2s" {
		t.Errorf("Timeout = %q, want 2s", hc.Timeout)
	}
	if hc.StartPeriod != "10s" {
		t.Errorf("StartPeriod = %q, want 10s", hc.StartPeriod)
	}
	if hc.Port != 5432 {
		t.Errorf("Port = %d, want 5432", hc.Port)
	}
}

func TestApplyDefaultsNoHealthcheck(t *testing.T) {
	data := []byte(`{
		"name": "test-net",
		"networks": { "backend": { "subnet": "192.168.51.0/24" } },
		"services": { "db": { "networks": [ { "name": "backend" } ] } }
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if c.Services["db"].Healthcheck != nil {
		t.Errorf("Healthcheck = %+v, want nil", c.Services["db"].Healthcheck)
	}
}
