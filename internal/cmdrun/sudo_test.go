package cmdrun

import (
	"reflect"
	"testing"
)

func TestSudoWrapsPrivilegedAsNonRoot(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 1000 }
	defer func() { geteuid = orig }()

	var names []string
	var argSets [][]string
	r := Sudo(func(name string, args ...string) error {
		names = append(names, name)
		argSets = append(argSets, append([]string(nil), args...))
		return nil
	}, "ip", "iptables")

	_ = r("ip", "link", "show")
	_ = r("qemu-img", "info")

	if !reflect.DeepEqual(names, []string{"sudo", "qemu-img"}) {
		t.Errorf("names = %v, want [sudo qemu-img]", names)
	}
	if !reflect.DeepEqual(argSets[0], []string{"-n", "ip", "link", "show"}) {
		t.Errorf("privileged args = %v, want [-n ip link show]", argSets[0])
	}
	if !reflect.DeepEqual(argSets[1], []string{"info"}) {
		t.Errorf("non-privileged args = %v, want [info]", argSets[1])
	}
}

func TestSudoNoWrapAsRoot(t *testing.T) {
	orig := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = orig }()

	var names []string
	r := Sudo(func(name string, args ...string) error {
		names = append(names, name)
		return nil
	}, "ip")

	_ = r("ip", "link", "show")
	if !reflect.DeepEqual(names, []string{"ip"}) {
		t.Errorf("names = %v, want [ip] (no sudo as root)", names)
	}
}
