package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "stackorder_session"

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
