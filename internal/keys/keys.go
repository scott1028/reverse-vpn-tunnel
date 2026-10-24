// Package keys generates the shared api_key and derives WireGuard keys from it.
package keys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

const salt = "reverse-vpn-tunnel/v1"

type Key [32]byte

// Keys holds both static key pairs and the preshared key. Never log it.
type Keys struct {
	ServerPrivate Key
	ServerPublic  Key
	ClientPrivate Key
	ClientPublic  Key
	PresharedKey  Key
}

// GenerateAPIKey returns 32 random bytes as base64url without padding.
func GenerateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Derive expands apiKey into both private keys and the PSK, so both sides
// know each other's public key without an exchange.
func Derive(apiKey string) (Keys, error) {
	master := argon2.IDKey([]byte(apiKey), []byte(salt), 3, 64*1024, 4, 32)
	var k Keys
	for _, out := range []struct {
		info string
		key  *Key
	}{
		{"server private key", &k.ServerPrivate},
		{"client private key", &k.ClientPrivate},
		{"preshared key", &k.PresharedKey},
	} {
		if _, err := io.ReadFull(hkdf.Expand(sha256.New, master, []byte(out.info)), out.key[:]); err != nil {
			return Keys{}, err
		}
	}
	clamp(&k.ServerPrivate)
	clamp(&k.ClientPrivate)
	var err error
	if k.ServerPublic, err = publicKey(k.ServerPrivate); err != nil {
		return Keys{}, err
	}
	if k.ClientPublic, err = publicKey(k.ClientPrivate); err != nil {
		return Keys{}, err
	}
	return k, nil
}

func clamp(k *Key) {
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
}

func publicKey(priv Key) (Key, error) {
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return Key{}, err
	}
	return Key(pub), nil
}
