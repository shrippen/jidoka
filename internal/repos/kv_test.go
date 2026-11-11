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
