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

func TestApplyDefaultsVolumeTypeDefaultsToShare(t *testing.T) {
	data := []byte(`{
		"name": "test-net",
		"networks": { "backend": { "subnet": "192.168.51.0/24" } },
		"services": {
			"db": {
				"networks": [ { "name": "backend" } ],
				"volumes": [ { "name": "data", "host": "/srv/data", "target": "/data" } ]
			}
		}
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	v := c.Services["db"].Volumes[0]
	if v.Type != "share" {
		t.Errorf("Type = %q, want share", v.Type)
	}
	if v.Protocol != "virtiofs" {
		t.Errorf("Protocol = %q, want virtiofs", v.Protocol)
	}
	if v.Mode != "rw" {
		t.Errorf("Mode = %q, want rw", v.Mode)
	}
}

func TestApplyDefaultsDiskVolumeKeepsExplicitType(t *testing.T) {
	data := []byte(`{
		"name": "test-net",
		"networks": { "backend": { "subnet": "192.168.51.0/24" } },
		"services": {
			"db": {
				"networks": [ { "name": "backend" } ],
				"volumes": [ { "type": "disk", "name": "data", "target": "/data", "size": "2G" } ]
			}
		}
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	v := c.Services["db"].Volumes[0]
	if v.Type != "disk" {
		t.Errorf("Type = %q, want disk", v.Type)
	}
	if v.FsType != "ext4" {
		t.Errorf("FsType = %q, want ext4", v.FsType)
	}
}

func TestApplyDefaultsExplicitProtocolNotOverridden(t *testing.T) {
	data := []byte(`{
		"name": "test-net",
		"networks": { "backend": { "subnet": "192.168.51.0/24" } },
		"services": {
			"db": {
				"networks": [ { "name": "backend" } ],
				"volumes": [ { "type": "share", "name": "data", "host": "/srv/data", "target": "/data", "protocol": "9p" } ]
			}
		}
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Services["db"].Volumes[0].Protocol; got != "9p" {
		t.Errorf("Protocol = %q, want 9p (explicit value preserved)", got)
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
