package sshkey

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type recorder struct {
	calls [][]string
	// pubBody is written to <privPath>.pub when ssh-keygen "runs", simulating
	// the real binary's side effect so EnsureKeypair can read it back.
	pubBody string
}

func (r *recorder) run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == "ssh-keygen" {
		privPath := args[len(args)-1]
		if err := os.WriteFile(privPath+".pub", []byte(r.pubBody), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func TestEnsureKeypairGeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{pubBody: "ssh-ed25519 AAAAfake microbe\n"}

	privPath, pub, err := EnsureKeypair(rec.run, dir)
	if err != nil {
		t.Fatal(err)
	}

	wantPriv := filepath.Join(dir, "id_ed25519")
	if privPath != wantPriv {
		t.Errorf("privPath = %q, want %q", privPath, wantPriv)
	}
	if pub != "ssh-ed25519 AAAAfake microbe" {
		t.Errorf("pub = %q", pub)
	}
	want := [][]string{
		{"ssh-keygen", "-t", "ed25519", "-N", "", "-C", "microbe", "-f", wantPriv},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Errorf("calls = %v, want %v", rec.calls, want)
	}
}

func TestEnsureKeypairSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(privPath, []byte("fake-priv"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privPath+".pub", []byte("ssh-ed25519 AAAAexisting microbe\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	gotPriv, pub, err := EnsureKeypair(rec.run, dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotPriv != privPath {
		t.Errorf("privPath = %q, want %q", gotPriv, privPath)
	}
	if pub != "ssh-ed25519 AAAAexisting microbe" {
		t.Errorf("pub = %q", pub)
	}
	if len(rec.calls) != 0 {
		t.Errorf("ssh-keygen invoked for existing key: %v", rec.calls)
	}
}
