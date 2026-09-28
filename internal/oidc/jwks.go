package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
)

const (
	maxJWKSBytes = 1 << 20
	minKeyBits   = 2048
)

type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jsonWebKeySet struct {
	Keys []jsonWebKey `json:"keys"`
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	// Detached so that a caller hanging up cannot fail the fetch and throttle every other caller.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrJWKSUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrJWKSUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET %s: status %d", ErrJWKSUnavailable, v.cfg.JWKSURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", ErrJWKSUnavailable, v.cfg.JWKSURL, err)
	}
	if len(body) > maxJWKSBytes {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrJWKSUnavailable, v.cfg.JWKSURL, maxJWKSBytes)
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrJWKSUnavailable, v.cfg.JWKSURL, err)
	}
	return keys, nil
}

func parseJWKS(data []byte) (map[string]*rsa.PublicKey, error) {
	var set jsonWebKeySet
	if err := json.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("decoding key set: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		if _, dup := keys[k.Kid]; dup {
			continue
		}
		pub, err := rsaPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("key set has no usable RS256 signing key")
	}
	return keys, nil
}

func rsaPublicKey(n, e string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(n, "="))
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(e, "="))
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	if len(eb) == 0 || len(eb) > 4 {
		return nil, fmt.Errorf("exponent is %d bytes", len(eb))
	}
	exp := 0
	for _, b := range eb {
		exp = exp<<8 | int(b)
	}
	if exp < 3 || exp%2 == 0 {
		return nil, fmt.Errorf("exponent %d is invalid", exp)
	}
	mod := new(big.Int).SetBytes(nb)
	if mod.BitLen() < minKeyBits {
		return nil, fmt.Errorf("modulus is %d bits, want at least %d", mod.BitLen(), minKeyBits)
	}
	return &rsa.PublicKey{N: mod, E: exp}, nil
}
