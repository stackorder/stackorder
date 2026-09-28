package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "stackorder_session"

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *server) sign(value string) string {
	m := hmac.New(sha256.New, s.sessionKey)
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *server) sessionCookieValue(token string) string {
	return token + "." + s.sign(token)
}

func (s *server) openSessionCookie(value string) (string, bool) {
	i := strings.LastIndexByte(value, '.')
	if i <= 0 {
		return "", false
	}
	token, sig := value[:i], value[i+1:]
	got, err := hex.DecodeString(sig)
	if err != nil {
		return "", false
	}
	want, _ := hex.DecodeString(s.sign(token))
	return token, hmac.Equal(got, want)
}

func (s *server) setCookie(w http.ResponseWriter, name, value, path string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   int(ttl / time.Second),
		Expires:  s.now().Add(ttl).UTC(),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

type sealed struct {
	Value   string `json:"v"`
	Next    string `json:"n,omitempty"`
	Expires int64  `json:"e"`
}

func (s *server) seal(purpose string, v sealed) string {
	data, _ := json.Marshal(v)
	payload := base64.RawURLEncoding.EncodeToString(data)
	return payload + "." + s.sign(purpose+"\x00"+payload)
}

func (s *server) unseal(purpose, cookie string) (sealed, bool) {
	payload, sig, ok := strings.Cut(cookie, ".")
	if !ok {
		return sealed{}, false
	}
	got, err := hex.DecodeString(sig)
	want, _ := hex.DecodeString(s.sign(purpose + "\x00" + payload))
	if err != nil || !hmac.Equal(got, want) {
		return sealed{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return sealed{}, false
	}
	var v sealed
	if err := json.Unmarshal(data, &v); err != nil || v.Value == "" || s.now().Unix() > v.Expires {
		return sealed{}, false
	}
	return v, true
}
