package keys

import (
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func TestGenerateAPIKey(t *testing.T) {
	a, err := GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateAPIKey()
	if a == b {
		t.Fatal("two generated keys are equal")
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key %q: decoded %d bytes, err %v", a, len(raw), err)
	}
}

func TestDerive(t *testing.T) {
	k1, err := Derive("an-api-key-of-enough-length")
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := Derive("an-api-key-of-enough-length")
	if k1 != k2 {
		t.Fatal("derivation is not deterministic")
	}
	other, _ := Derive("another-api-key-of-enough-length")
	if other.ServerPrivate == k1.ServerPrivate || other.PresharedKey == k1.PresharedKey {
		t.Fatal("different api keys derived the same keys")
	}
	if k1.ServerPrivate == k1.ClientPrivate || k1.ServerPrivate == k1.PresharedKey {
		t.Fatal("derived keys are not distinct")
	}
	for name, priv := range map[string]Key{"server": k1.ServerPrivate, "client": k1.ClientPrivate} {
		if priv[0]&7 != 0 || priv[31]&128 != 0 || priv[31]&64 == 0 {
			t.Errorf("%s private key not clamped", name)
		}
	}
	for _, c := range []struct{ priv, pub Key }{
		{k1.ServerPrivate, k1.ServerPublic},
		{k1.ClientPrivate, k1.ClientPublic},
	} {
		want, _ := curve25519.X25519(c.priv[:], curve25519.Basepoint)
		if Key(want) != c.pub {
			t.Error("public key does not match private key")
		}
	}
}
