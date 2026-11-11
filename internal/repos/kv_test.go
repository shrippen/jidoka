package repos

import "testing"

func TestKV(t *testing.T) {
	d := testDB(t)
	if _, ok, err := GetKV(d, "k"); ok || err != nil {
		t.Fatalf("unset: %v %v", ok, err)
	}
	must(t, SetKV(d, "k", "v1"))
	must(t, SetKV(d, "k", "v2"))
	if v, ok, err := GetKV(d, "k"); !ok || err != nil || v != "v2" {
		t.Fatalf("get: %q %v %v", v, ok, err)
	}
}

func TestSettings(t *testing.T) {
	d := testDB(t)
	if _, ok, err := GetSettings(d); ok || err != nil {
		t.Fatalf("unset: %v %v", ok, err)
	}
	must(t, PutSettings(d, "zone = \"UTC\"\n"))
	must(t, PutSettings(d, "zone = \"Europe/Berlin\"\n"))
	if v, ok, err := GetSettings(d); !ok || err != nil || v != "zone = \"Europe/Berlin\"\n" {
		t.Fatalf("get: %q %v %v", v, ok, err)
	}
}
