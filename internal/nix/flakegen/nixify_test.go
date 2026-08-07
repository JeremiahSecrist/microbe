package flakegen

import "testing"

func TestNixifyString(t *testing.T) {
	got := nixify("hello")
	if got != `"hello"` {
		t.Errorf("nixify(%q) = %s, want \"hello\"", "hello", got)
	}
}

func TestNixifyStringEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{`${nope}`, `"\${nope}"`},
	}
	for _, c := range cases {
		got := nixify(c.in)
		if got != c.want {
			t.Errorf("nixify(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestNixifyIntAndBool(t *testing.T) {
	if got := nixify(24); got != "24" {
		t.Errorf("nixify(24) = %s, want 24", got)
	}
	if got := nixify(true); got != "true" {
		t.Errorf("nixify(true) = %s, want true", got)
	}
}

func TestNixifyStringMapSorted(t *testing.T) {
	got := nixify(map[string]string{"z": "a", "a": "b"})
	want := "{\n  a = \"b\";\n  z = \"a\";\n}"
	if got != want {
		t.Errorf("nixify map:\n got: %s\nwant: %s", got, want)
	}
}

func TestNixifyEmptyAttrset(t *testing.T) {
	if got := nixify(map[string]string{}); got != "{}" {
		t.Errorf("nixify(empty) = %s, want {}", got)
	}
}

func TestNixifyNested(t *testing.T) {
	v := map[string]any{
		"address": []string{"192.168.51.2/24"},
		"routes":  []any{map[string]string{"Gateway": "192.168.51.1"}},
	}
	got := nixify(v)
	want := `{
  address = [ "192.168.51.2/24" ];
  routes = [
    {
      Gateway = "192.168.51.1";
    }
  ];
}`
	if got != want {
		t.Errorf("nixify nested:\n got: %s\nwant: %s", got, want)
	}
}

func TestNixifyListOfHosts(t *testing.T) {
	v := []any{map[string]any{
		"ip":    "192.168.51.2",
		"names": []string{"db", "db.backend"},
	}}
	got := nixify(v)
	want := `[
  {
    ip = "192.168.51.2";
    names = [ "db" "db.backend" ];
  }
]`
	if got != want {
		t.Errorf("nixify hosts:\n got: %s\nwant: %s", got, want)
	}
}
