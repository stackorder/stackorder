package stateread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const (
	stateVersion = 4
	maxDepth     = 10000
	outputCost   = 256
)

var (
	errObjectLimit  = errors.New("object limit")
	errOutputsLimit = errors.New("outputs limit")
	errValueLimit   = errors.New("value limit")
)

// Decode reads a state object from r and returns its identity and root
// module outputs. It streams the top-level object, skips every value other
// than version, serial, lineage, terraform_version and outputs one token at
// a time, and returns without reading further once it has the outputs of a
// version 4 state, which Terraform and OpenTofu write before resources. It
// fails with ErrTooLarge when r holds more than MaxObjectBytes before the
// outputs are complete, or the outputs exceed MaxOutputsBytes.
func Decode(r io.Reader) (*State, error) {
	return decode(r, MaxObjectBytes, MaxOutputsBytes)
}

func decode(r io.Reader, maxObject, maxOutputs int64) (*State, error) {
	src := &countingReader{r: r, window: maxOutputs}
	src.limitTo(maxObject, errObjectLimit)
	d := &decoder{dec: json.NewDecoder(src), src: src, maxObject: maxObject, maxOutputs: maxOutputs}
	d.dec.UseNumber()
	src.consumed = d.dec.InputOffset
	return d.state()
}

type decoder struct {
	dec        *json.Decoder
	src        *countingReader
	maxObject  int64
	maxOutputs int64
	buf        bytes.Buffer
}

type rawOutput struct {
	Value     json.RawMessage `json:"value"`
	Type      json.RawMessage `json:"type"`
	Sensitive bool            `json:"sensitive"`
}

func (d *decoder) state() (*State, error) {
	if err := d.delim('{'); err != nil {
		return nil, err
	}
	var (
		st                  State
		version             uint64
		hasVersion, hasOuts bool
	)
	for d.dec.More() {
		key, err := d.key()
		if err != nil {
			return nil, err
		}
		switch key {
		case "version":
			at := d.dec.InputOffset()
			if version, err = d.uint(); err == nil && version != stateVersion {
				return nil, fmt.Errorf("%w: not version %d at byte %d", ErrUnsupported, stateVersion, at)
			}
			hasVersion = true
		case "serial":
			st.Serial, err = d.uint()
		case "lineage":
			st.Lineage, err = d.string()
		case "terraform_version":
			st.TerraformVersion, err = d.string()
		case "outputs":
			st.Outputs, err = d.outputs()
			hasOuts = true
		case "encrypted_data", "encryption_version":
			return nil, ErrEncrypted
		default:
			err = d.skip()
		}
		if err != nil {
			return nil, err
		}
		if hasVersion && hasOuts {
			return &st, nil
		}
	}
	if err := d.delim('}'); err != nil {
		return nil, err
	}
	if !hasVersion {
		return nil, fmt.Errorf("%w: no version", ErrUnsupported)
	}
	st.Outputs = map[string]Output{}
	return &st, nil
}

func (d *decoder) outputs() (map[string]Output, error) {
	start := d.dec.InputOffset()
	if start+d.maxOutputs < d.maxObject {
		d.src.limitTo(start+d.maxOutputs, errOutputsLimit)
		defer d.src.limitTo(d.maxObject, errObjectLimit)
	}
	tok, err := d.dec.Token()
	if err != nil {
		return nil, d.fail(err)
	}
	outs := map[string]Output{}
	if tok == nil {
		return outs, nil
	}
	if tok != json.Delim('{') {
		return nil, d.malformed("outputs is not an object")
	}
	var (
		raw  rawOutput
		size int64
	)
	for d.dec.More() {
		name, err := d.key()
		if err != nil {
			return nil, err
		}
		at := d.dec.InputOffset()
		raw = rawOutput{Value: raw.Value[:0], Type: raw.Type[:0]}
		if err := d.dec.Decode(&raw); err != nil {
			return nil, d.fail(err)
		}
		if d.dec.InputOffset()-start > d.maxOutputs {
			return nil, d.fail(errOutputsLimit)
		}
		if len(raw.Value) == 0 {
			return nil, fmt.Errorf("%w: output without a value at byte %d", ErrMalformed, at)
		}
		if len(raw.Type) == 0 || string(raw.Type) == "null" {
			return nil, fmt.Errorf("%w: output without a type at byte %d", ErrMalformed, at)
		}
		o := Output{Value: d.compact(raw.Value), Type: d.compact(raw.Type), Sensitive: raw.Sensitive}
		if size += int64(len(name)+len(o.Value)+len(o.Type)) + outputCost; size > d.maxOutputs {
			return nil, d.fail(errOutputsLimit)
		}
		outs[name] = o
	}
	if err := d.delim('}'); err != nil {
		return nil, err
	}
	return outs, nil
}

func (d *decoder) compact(raw json.RawMessage) json.RawMessage {
	d.buf.Reset()
	if err := json.Compact(&d.buf, raw); err != nil {
		return bytes.Clone(raw)
	}
	return bytes.Clone(d.buf.Bytes())
}

func (d *decoder) skip() error {
	depth := 0
	for {
		tok, err := d.dec.Token()
		if err != nil {
			return d.fail(err)
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			if depth++; depth > maxDepth {
				return d.malformed("nested too deeply")
			}
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}

func (d *decoder) delim(want json.Delim) error {
	tok, err := d.dec.Token()
	if err != nil {
		return d.fail(err)
	}
	if tok != want {
		return d.malformed("unexpected token")
	}
	return nil
}

func (d *decoder) key() (string, error) {
	tok, err := d.dec.Token()
	if err != nil {
		return "", d.fail(err)
	}
	key, ok := tok.(string)
	if !ok {
		return "", d.malformed("unexpected token")
	}
	return key, nil
}

func (d *decoder) string() (string, error) {
	tok, err := d.dec.Token()
	if err != nil {
		return "", d.fail(err)
	}
	s, ok := tok.(string)
	if !ok {
		return "", d.malformed("expected a string")
	}
	return s, nil
}

func (d *decoder) uint() (uint64, error) {
	tok, err := d.dec.Token()
	if err != nil {
		return 0, d.fail(err)
	}
	n, ok := tok.(json.Number)
	if !ok {
		return 0, d.malformed("expected a number")
	}
	v, err := strconv.ParseUint(n.String(), 10, 64)
	if err != nil {
		return 0, d.malformed("expected an unsigned integer")
	}
	return v, nil
}

func (d *decoder) malformed(what string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrMalformed, what, d.dec.InputOffset())
}

func (d *decoder) fail(err error) error {
	switch {
	case errors.Is(err, errObjectLimit):
		return fmt.Errorf("%w: no outputs in the first %d bytes", ErrTooLarge, d.maxObject)
	case errors.Is(err, errOutputsLimit):
		return fmt.Errorf("%w: outputs exceed %d bytes", ErrTooLarge, d.maxOutputs)
	case errors.Is(err, errValueLimit):
		return fmt.Errorf("%w: a value at byte %d exceeds %d bytes", ErrTooLarge, d.dec.InputOffset(), d.maxOutputs)
	case d.src.readErr != nil:
		return fmt.Errorf("stateread: read state at byte %d: %w", d.src.n, d.src.readErr)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("%w: unexpected end at byte %d", ErrMalformed, d.src.n)
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return fmt.Errorf("%w: invalid JSON at byte %d", ErrMalformed, syntax.Offset)
	}
	return d.malformed("unexpected value")
}

type countingReader struct {
	r        io.Reader
	n        int64
	limit    int64
	limitErr error
	window   int64
	consumed func() int64
	err      error
	readErr  error
}

func (c *countingReader) limitTo(limit int64, err error) {
	c.limit, c.limitErr = limit, err
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.consumed != nil {
		room := c.window - (c.n - c.consumed())
		if room <= 0 {
			c.err = errValueLimit
			return 0, c.err
		}
		p = p[:min(int64(len(p)), room)]
	}
	room := c.limit - c.n
	if room <= 0 {
		return c.probe()
	}
	k, err := c.r.Read(p[:min(int64(len(p)), room)])
	c.n += int64(k)
	if err != nil && !errors.Is(err, io.EOF) {
		c.err, c.readErr = err, err
	}
	return k, err
}

func (c *countingReader) probe() (int, error) {
	var b [1]byte
	k, err := c.r.Read(b[:])
	switch {
	case k > 0:
		c.err = c.limitErr
	case err != nil && !errors.Is(err, io.EOF):
		c.err, c.readErr = err, err
	default:
		return 0, err
	}
	return 0, c.err
}
