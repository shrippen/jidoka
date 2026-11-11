package repos

import (
	"bytes"
	"testing"
	"time"
)

func TestSecrets(t *testing.T) {
	d := testDB(t)
	must(t, PutSecret(d, "B_KEY", []byte("enc-b"), "arian", t0))
	must(t, PutSecret(d, "A_KEY", []byte("enc-a"), "arian", t0))
	must(t, PutSecret(d, "A_KEY", []byte("enc-a2"), "ines", t0.Add(time.Hour)))

	list, err := ListSecrets(d)
	must(t, err)
	if len(list) != 2 || list[0].Name != "A_KEY" || list[0].By != "ines" || !list[0].Updated.Equal(t0.Add(time.Hour)) {
		t.Fatalf("list: %+v", list)
	}

	enc, err := GetSecretEnc(d, "A_KEY")
	must(t, err)
	if !bytes.Equal(enc, []byte("enc-a2")) {
		t.Fatalf("enc: %q", enc)
	}
	if _, err := GetSecretEnc(d, "NONE"); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}

	must(t, ReplaceSecretEnc(d, "A_KEY", []byte("rotated")))
	all, err := AllSecretsEnc(d)
	must(t, err)
	if len(all) != 2 || string(all[0].Enc) != "rotated" {
		t.Fatalf("all: %+v", all)
	}
	if l, _ := ListSecrets(d); l[0].By != "ines" {
		t.Fatal("rotation changed the author")
	}

	must(t, DeleteSecret(d, "A_KEY"))
	if err := DeleteSecret(d, "A_KEY"); err != ErrNotFound {
		t.Fatalf("second delete: %v", err)
	}
}
