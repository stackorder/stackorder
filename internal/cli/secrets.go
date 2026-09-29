package cli

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	v1 "github.com/stackorder/stackorder/api/v1"
	"github.com/stackorder/stackorder/internal/tf"
)

var (
	sensitiveEnvName = regexp.MustCompile(`(?i)(^|_)(SECRET|TOKEN|PASSWORD|PASSWD|PASSPHRASE|APIKEY|API_KEY|PRIVATE_KEY|CREDENTIALS?)(_|$)`)
	pemBegin         = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
	pemEnd           = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
)

func envSecrets() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !sensitiveEnvName.MatchString(name) || plainValue(value) {
			continue
		}
		out = append(out, value)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func configuredSecrets(env map[v1.RunMode]map[string]string) []string {
	var out []string
	for _, vars := range env {
		for name, value := range vars {
			if sensitiveEnvName.MatchString(name) && !plainValue(value) {
				out = append(out, value)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func plainValue(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < tf.MinSecretLength {
		return true
	}
	if _, err := strconv.ParseBool(v); err == nil {
		return true
	}
	_, err := strconv.ParseFloat(v, 64)
	return err == nil
}

func redactedValues(orig, red string) []string {
	if orig == red {
		return nil
	}
	parts := strings.Split(red, tf.Mask)
	if len(parts) < 2 || !strings.HasPrefix(orig, parts[0]) {
		return nil
	}
	pos := len(parts[0])
	rest := parts[1:]
	var out []string
	for len(rest) > 0 {
		k := 0
		for k < len(rest)-1 && rest[k] == "" {
			k++
		}
		seg := rest[k]
		var end int
		if k == len(rest)-1 {
			if !strings.HasSuffix(orig, seg) || len(orig)-len(seg) < pos {
				return out
			}
			end = len(orig) - len(seg)
		} else {
			j := strings.Index(orig[pos:], seg)
			if j < 0 {
				return out
			}
			end = pos + j
		}
		if end > pos {
			out = append(out, orig[pos:end])
		}
		pos = end + len(seg)
		rest = rest[k+1:]
	}
	return out
}

type maskWriter struct {
	mu    sync.Mutex
	w     io.Writer
	r     *tf.Redactor
	seen  map[string]bool
	buf   []byte
	inPEM bool
}

func newMaskWriter(w io.Writer, r *tf.Redactor, known []string) *maskWriter {
	m := &maskWriter{w: w, r: r, seen: map[string]bool{}}
	for _, s := range known {
		m.seen[s] = true
	}
	return m
}

func (m *maskWriter) setRedactor(r *tf.Redactor, known []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.r = r
	for _, s := range known {
		m.seen[s] = true
	}
}

func (m *maskWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf = append(m.buf, p...)
	for {
		i := bytes.IndexByte(m.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(m.buf[:i+1])
		m.buf = m.buf[i+1:]
		if err := m.writeLine(line); err != nil {
			return 0, err
		}
	}
}

func (m *maskWriter) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.buf) == 0 {
		return nil
	}
	line := string(m.buf)
	m.buf = nil
	return m.writeLine(line)
}

func (m *maskWriter) writeLine(line string) error {
	text, found := strings.CutSuffix(line, "\n")
	eol := ""
	if found {
		eol = "\n"
	}
	var secrets []string
	if m.inPEM {
		body, tail := text, ""
		if loc := pemEnd.FindStringIndex(text); loc != nil {
			body, tail = text[:loc[0]], text[loc[0]:]
			m.inPEM = false
		}
		if t := strings.TrimSpace(body); t != "" {
			secrets = append(secrets, t)
			body = body[:len(body)-len(strings.TrimLeft(body, " \t"))] + tf.Mask
		}
		text = body + tail
		if m.inPEM {
			return m.emit(secrets, text+eol)
		}
	}
	red := m.r.Redact(text)
	secrets = append(secrets, redactedValues(text, red)...)
	if begins := pemBegin.FindAllStringIndex(red, -1); len(begins) > 0 {
		last := begins[len(begins)-1]
		m.inPEM = pemEnd.FindStringIndex(red[last[1]:]) == nil
	}
	return m.emit(secrets, red+eol)
}

func (m *maskWriter) emit(secrets []string, line string) error {
	var fresh []string
	for _, s := range secrets {
		if s = strings.TrimSpace(s); s != "" && !m.seen[s] {
			m.seen[s] = true
			fresh = append(fresh, s)
		}
	}
	for _, cmd := range tf.MaskCommands(fresh) {
		if _, err := io.WriteString(m.w, cmd+"\n"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(m.w, line)
	return err
}

func (a *app) redactMessage(msg string) string {
	return tf.NewRedactor(append(envSecrets(), a.secrets...)).Redact(msg)
}
