package stateread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	stateVersion     = 4
	outputCost       = 256
	bufferSize       = 32 << 10
	maxIdentityBytes = 256
	maxFieldBytes    = 128
)

var (
	errObjectLimit  = errors.New("object limit")
	errOutputsLimit = errors.New("outputs limit")
	errValueLimit   = errors.New("value limit")
)

// Decode reads a state object from r and returns its identity and root
// module outputs. It streams the top-level object through a fixed buffer,
// checks and skips every value other than version, serial, lineage,
// terraform_version and outputs byte by byte without keeping it, and
// returns without reading further once it has the outputs of a version 4
// state, which Terraform and OpenTofu write before resources. It fails with
// ErrTooLarge when r holds more than MaxObjectBytes before the outputs are
// complete, the outputs exceed MaxOutputsBytes, or a string or number
// before them does, and with ErrMalformed when the lineage or Terraform
// version is longer than 256 bytes.
func Decode(r io.Reader) (*State, error) {
	return decode(r, MaxObjectBytes, MaxOutputsBytes)
}

func decode(r io.Reader, maxObject, maxOutputs int64) (*State, error) {
	d := newDecoder(r, maxObject, maxOutputs)
	st, err := d.state()
	if err != nil {
		return nil, d.fail(err)
	}
	return st, nil
}

func newDecoder(r io.Reader, maxObject, maxOutputs int64) *decoder {
	d := &decoder{r: r, buf: make([]byte, bufferSize), maxObject: maxObject, maxOutputs: maxOutputs}
	d.limitTo(maxObject, errObjectLimit)
	return d
}

type decoder struct {
	r          io.Reader
	buf        []byte
	pos        int
	end        int
	n          int
	off        int64
	limit      int64
	limitErr   error
	err        error
	maxObject  int64
	maxOutputs int64
	limitAt    int64
	stack      []byte
	key        []byte
	scratch    []byte
}

func (d *decoder) state() (*State, error) {
	if err := d.expect('{'); err != nil {
		return nil, err
	}
	var (
		st                  State
		version             uint64
		hasVersion, hasOuts bool
	)
	for first := true; ; first = false {
		more, err := d.member(first)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
		key, err := d.field()
		if err != nil {
			return nil, err
		}
		if string(key) == "encrypted_data" || string(key) == "encryption_version" {
			return nil, ErrEncrypted
		}
		if err := d.colon(); err != nil {
			return nil, err
		}
		switch string(key) {
		case "version":
			at := d.offset()
			if version, err = d.uint(); err == nil && version != stateVersion {
				return nil, fmt.Errorf("%w: not version %d at byte %d", ErrUnsupported, stateVersion, at)
			}
			hasVersion = true
		case "serial":
			st.Serial, err = d.uint()
		case "lineage":
			st.Lineage, err = d.identity("lineage")
		case "terraform_version":
			st.TerraformVersion, err = d.identity("terraform_version")
		case "outputs":
			st.Outputs = nil
			st.Outputs, err = d.outputs()
			hasOuts = true
		default:
			_, err = d.value(nil, false)
		}
		if err != nil {
			return nil, err
		}
		if hasVersion && hasOuts {
			return &st, nil
		}
	}
	if !hasVersion {
		return nil, fmt.Errorf("%w: no version", ErrUnsupported)
	}
	st.Outputs = map[string]Output{}
	return &st, nil
}

func (d *decoder) outputs() (map[string]Output, error) {
	c, err := d.peek()
	if err != nil {
		return nil, err
	}
	if start := d.offset(); start+d.maxOutputs < d.maxObject {
		d.limitTo(start+d.maxOutputs, errOutputsLimit)
		defer d.limitTo(d.maxObject, errObjectLimit)
	}
	outs := map[string]Output{}
	if c == 'n' {
		_, err := d.literal(nil, false, "null")
		return outs, err
	}
	if c != '{' {
		return nil, d.malformed("outputs is not an object")
	}
	d.pos++
	var size int64
	for first := true; ; first = false {
		more, err := d.member(first)
		if err != nil {
			return nil, err
		}
		if !more {
			return outs, nil
		}
		name, at, err := d.name()
		if err != nil {
			return nil, err
		}
		if err := d.colon(); err != nil {
			return nil, err
		}
		o, err := d.output(at)
		if err != nil {
			return nil, err
		}
		if size += int64(len(name)+len(o.Value)+len(o.Type)) + outputCost; size > d.maxOutputs {
			return nil, errOutputsLimit
		}
		outs[name] = o
	}
}

func (d *decoder) output(at int64) (Output, error) {
	var o Output
	c, err := d.peek()
	if err != nil {
		return o, err
	}
	if c == 'n' {
		if _, err := d.literal(nil, false, "null"); err != nil {
			return o, err
		}
		return o, fmt.Errorf("%w: output without a value at byte %d", ErrMalformed, at)
	}
	if c != '{' {
		return o, d.malformed("output is not an object")
	}
	d.pos++
	for first := true; ; first = false {
		more, err := d.member(first)
		if err != nil {
			return o, err
		}
		if !more {
			break
		}
		field, err := d.field()
		if err != nil {
			return o, err
		}
		if err := d.colon(); err != nil {
			return o, err
		}
		switch {
		case bytes.EqualFold(field, []byte("value")):
			o.Value, err = d.compact()
		case bytes.EqualFold(field, []byte("type")):
			o.Type, err = d.compact()
		case bytes.EqualFold(field, []byte("sensitive")):
			o.Sensitive, err = d.bool(o.Sensitive)
		default:
			_, err = d.value(nil, false)
		}
		if err != nil {
			return o, err
		}
	}
	if len(o.Value) == 0 {
		return o, fmt.Errorf("%w: output without a value at byte %d", ErrMalformed, at)
	}
	if len(o.Type) == 0 || string(o.Type) == "null" {
		return o, fmt.Errorf("%w: output without a type at byte %d", ErrMalformed, at)
	}
	return o, nil
}

func (d *decoder) compact() (json.RawMessage, error) {
	var err error
	if d.scratch, err = d.value(d.scratch[:0], true); err != nil {
		return nil, err
	}
	return bytes.Clone(d.scratch), nil
}

func (d *decoder) member(first bool) (bool, error) {
	c, err := d.peek()
	if err != nil {
		return false, err
	}
	if c == '}' {
		d.pos++
		return false, nil
	}
	if !first {
		if c != ',' {
			return false, d.malformed("unexpected token")
		}
		d.pos++
		if c, err = d.peek(); err != nil {
			return false, err
		}
	}
	if c != '"' {
		return false, d.malformed("unexpected token")
	}
	return true, nil
}

func (d *decoder) name() (string, int64, error) {
	var err error
	if d.key, err = d.str(d.key[:0], true, d.maxOutputs); err != nil {
		return "", 0, err
	}
	at := d.offset()
	name, err := d.unquote(d.key)
	if err != nil {
		return "", 0, err
	}
	return name, at, nil
}

func (d *decoder) field() ([]byte, error) {
	start := d.offset()
	var err error
	if d.key, err = d.str(d.key[:0], true, min(maxFieldBytes, d.maxOutputs)); err == nil {
		return unescape(d.key[1 : len(d.key)-1]), nil
	}
	if !errors.Is(err, errValueLimit) {
		return nil, err
	}
	_, err = d.strFrom(start, nil, false, d.maxOutputs)
	return nil, err
}

func unescape(b []byte) []byte {
	i := bytes.IndexByte(b, '\\')
	if i < 0 {
		return b
	}
	out := b[:i]
	for i < len(b) {
		if b[i] != '\\' {
			out = append(out, b[i])
			i++
			continue
		}
		if b[i+1] != 'u' {
			out = append(out, escaped[b[i+1]])
			i += 2
			continue
		}
		r := hexRune(b[i+2 : i+6])
		i += 6
		if utf16.IsSurrogate(r) {
			r2 := unicode.ReplacementChar
			if i+6 <= len(b) && b[i] == '\\' && b[i+1] == 'u' {
				r2 = hexRune(b[i+2 : i+6])
			}
			if r = utf16.DecodeRune(r, r2); r != unicode.ReplacementChar {
				i += 6
			}
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

var escaped = [256]byte{'"': '"', '\\': '\\', '/': '/', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t'}

func hexRune(h []byte) rune {
	var r rune
	for _, c := range h {
		switch {
		case c <= '9':
			c -= '0'
		case c <= 'F':
			c -= 'A' - 10
		default:
			c -= 'a' - 10
		}
		r = r<<4 | rune(c)
	}
	return r
}

func (d *decoder) unquote(quoted []byte) (string, error) {
	if bytes.IndexByte(quoted, '\\') < 0 && utf8.Valid(quoted) {
		return string(quoted[1 : len(quoted)-1]), nil
	}
	var s string
	if err := json.Unmarshal(quoted, &s); err != nil {
		return "", d.malformed("invalid JSON")
	}
	return s, nil
}

func (d *decoder) identity(what string) (string, error) {
	c, err := d.peek()
	if err != nil {
		return "", err
	}
	if c != '"' {
		return "", d.malformed("expected a string")
	}
	at, limit := d.offset(), int64(maxIdentityBytes+2)
	d.key, err = d.str(d.key[:0], true, min(limit, d.maxOutputs))
	if errors.Is(err, errValueLimit) && limit <= d.maxOutputs {
		return "", fmt.Errorf("%w: %s longer than %d bytes at byte %d", ErrMalformed, what, maxIdentityBytes, at)
	}
	if err != nil {
		return "", err
	}
	return d.unquote(d.key)
}

func (d *decoder) uint() (uint64, error) {
	c, err := d.peek()
	if err != nil {
		return 0, err
	}
	if c != '-' && (c < '0' || c > '9') {
		return 0, d.malformed("expected a number")
	}
	if d.key, err = d.number(d.key[:0], true); err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(string(d.key), 10, 64)
	if err != nil {
		return 0, d.malformed("expected an unsigned integer")
	}
	return v, nil
}

func (d *decoder) bool(previous bool) (bool, error) {
	c, err := d.peek()
	if err != nil {
		return false, err
	}
	switch c {
	case 't':
		_, err = d.literal(nil, false, "true")
		return true, err
	case 'f':
		_, err = d.literal(nil, false, "false")
		return false, err
	case 'n':
		_, err = d.literal(nil, false, "null")
		return previous, err
	}
	return false, d.malformed("unexpected value")
}

func (d *decoder) malformed(what string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrMalformed, what, d.offset())
}

func (d *decoder) tooLarge(at int64) error {
	d.limitAt = at
	return errValueLimit
}

func (d *decoder) fail(err error) error {
	switch {
	case errors.Is(err, errObjectLimit):
		return fmt.Errorf("%w: no outputs in the first %d bytes", ErrTooLarge, d.maxObject)
	case errors.Is(err, errOutputsLimit):
		return fmt.Errorf("%w: outputs exceed %d bytes", ErrTooLarge, d.maxOutputs)
	case errors.Is(err, errValueLimit):
		return fmt.Errorf("%w: a value at byte %d exceeds %d bytes", ErrTooLarge, d.limitAt, d.maxOutputs)
	case errors.Is(err, io.EOF):
		return fmt.Errorf("%w: unexpected end at byte %d", ErrMalformed, d.off+int64(d.n))
	}
	return err
}
