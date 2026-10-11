package stateread

import (
	"errors"
	"fmt"
	"io"
)

const (
	maxDepth     = 10000
	maxEmptyRead = 100
)

type expectation uint8

const (
	expectValue expectation = iota
	expectValueOrEnd
	expectKey
	expectKeyOrEnd
	expectColon
	expectNext
)

func (d *decoder) value(dst []byte, keep bool) ([]byte, error) {
	base := len(d.stack)
	defer func() { d.stack = d.stack[:base] }()
	want := expectValue
	for {
		if d.pos >= d.end {
			if err := d.more(); err != nil {
				return dst, err
			}
		}
		c := d.buf[d.pos]
		if c <= ' ' {
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				return dst, d.malformed("invalid JSON")
			}
			d.pos++
			continue
		}
		var err error
		switch {
		case want == expectColon:
			if c != ':' {
				return dst, d.malformed("invalid JSON")
			}
			want = expectValue
		case want == expectNext && c == ',':
			want = expectValue
			if d.stack[len(d.stack)-1] == '{' {
				want = expectKey
			}
		case (want == expectNext || want == expectValueOrEnd || want == expectKeyOrEnd) && c == d.stack[len(d.stack)-1]+2:
			d.stack = d.stack[:len(d.stack)-1]
			want = expectNext
		case want == expectKey || want == expectKeyOrEnd:
			if c != '"' {
				return dst, d.malformed("invalid JSON")
			}
			if dst, err = d.str(dst, keep, d.maxOutputs); err != nil {
				return dst, err
			}
			want = expectColon
			continue
		case want == expectNext:
			return dst, d.malformed("invalid JSON")
		case c == '{' || c == '[':
			if len(d.stack)-base >= maxDepth {
				return dst, d.malformed("nested too deeply")
			}
			d.stack = append(d.stack, c)
			want = expectValueOrEnd
			if c == '{' {
				want = expectKeyOrEnd
			}
		default:
			switch {
			case c == '"':
				dst, err = d.str(dst, keep, d.maxOutputs)
			case c == '-' || c >= '0' && c <= '9':
				dst, err = d.number(dst, keep)
			case c == 't':
				dst, err = d.literal(dst, keep, "true")
			case c == 'f':
				dst, err = d.literal(dst, keep, "false")
			case c == 'n':
				dst, err = d.literal(dst, keep, "null")
			default:
				return dst, d.malformed("invalid JSON")
			}
			if err != nil {
				return dst, err
			}
			want = expectNext
			if len(d.stack) == base {
				return dst, nil
			}
			continue
		}
		d.pos++
		if keep {
			dst = append(dst, c)
		}
		if want == expectNext && len(d.stack) == base {
			return dst, nil
		}
	}
}

func (d *decoder) str(dst []byte, keep bool, limit int64) ([]byte, error) {
	start := d.offset()
	d.pos++
	if keep {
		dst = append(dst, '"')
	}
	return d.strFrom(start, dst, keep, limit)
}

func (d *decoder) strFrom(start int64, dst []byte, keep bool, limit int64) ([]byte, error) {
	for {
		if d.offset()-start >= limit {
			return dst, d.tooLarge(start)
		}
		if err := d.more(); err != nil {
			return dst, err
		}
		stop := d.end
		if room := start + limit - d.off; room < int64(stop) {
			stop = int(room)
		}
		run := d.buf[d.pos:stop]
		n := 0
		for n < len(run) {
			if c := run[n]; c == '"' || c == '\\' || c < 0x20 {
				break
			}
			n++
		}
		if keep {
			dst = append(dst, run[:n]...)
		}
		d.pos += n
		if n == len(run) {
			continue
		}
		switch run[n] {
		case '"':
			d.pos++
			if keep {
				dst = append(dst, '"')
			}
			return dst, nil
		case '\\':
			var err error
			if dst, err = d.escape(dst, keep); err != nil {
				return dst, err
			}
		default:
			return dst, d.malformed("invalid JSON")
		}
	}
}

func (d *decoder) escape(dst []byte, keep bool) ([]byte, error) {
	d.pos++
	c, err := d.next()
	if err != nil {
		return dst, err
	}
	if keep {
		dst = append(dst, '\\', c)
	}
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return dst, nil
	case 'u':
		for range 4 {
			h, err := d.next()
			if err != nil {
				return dst, err
			}
			if !isHex(h) {
				return dst, d.malformed("invalid JSON")
			}
			if keep {
				dst = append(dst, h)
			}
		}
		return dst, nil
	}
	return dst, d.malformed("invalid JSON")
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

type numState uint8

const (
	numStart numState = iota
	numMinus
	numZero
	numInt
	numDot
	numFrac
	numE
	numESign
	numExp
	numEnd
	numBad
)

func (s numState) step(c byte) numState {
	digit := c >= '0' && c <= '9'
	switch s {
	case numStart:
		switch {
		case c == '-':
			return numMinus
		case c == '0':
			return numZero
		case digit:
			return numInt
		}
	case numMinus:
		switch {
		case c == '0':
			return numZero
		case digit:
			return numInt
		}
	case numZero, numInt:
		switch {
		case digit && s == numInt:
			return numInt
		case c == '.':
			return numDot
		case c == 'e' || c == 'E':
			return numE
		}
		return numEnd
	case numDot, numFrac:
		switch {
		case digit:
			return numFrac
		case s == numFrac && (c == 'e' || c == 'E'):
			return numE
		case s == numFrac:
			return numEnd
		}
	case numE:
		switch {
		case c == '+' || c == '-':
			return numESign
		case digit:
			return numExp
		}
	case numESign, numExp:
		switch {
		case digit:
			return numExp
		case s == numExp:
			return numEnd
		}
	}
	return numBad
}

var numSteps = func() (t [numBad + 1][256]numState) {
	for s := range t {
		for c := range t[s] {
			t[s][c] = numState(s).step(byte(c))
		}
	}
	return t
}()

func (s numState) complete() bool {
	return s == numZero || s == numInt || s == numFrac || s == numExp
}

func (d *decoder) number(dst []byte, keep bool) ([]byte, error) {
	start := d.offset()
	state := numStart
	for state != numEnd {
		if err := d.more(); err != nil {
			if errors.Is(err, io.EOF) && state.complete() {
				break
			}
			return dst, err
		}
		buf, i := d.buf[:d.end], d.pos
		over := start + d.maxOutputs - d.off
		for ; i < len(buf); i++ {
			next := numSteps[state][buf[i]]
			if next == numEnd {
				state = numEnd
				break
			}
			if next == numBad || int64(i) >= over {
				if keep {
					dst = append(dst, buf[d.pos:i]...)
				}
				d.pos = i
				if next == numBad {
					return dst, d.malformed("invalid JSON")
				}
				return dst, d.tooLarge(start)
			}
			state = next
		}
		if keep {
			dst = append(dst, buf[d.pos:i]...)
		}
		d.pos = i
	}
	return dst, nil
}

func (d *decoder) literal(dst []byte, keep bool, word string) ([]byte, error) {
	if d.end-d.pos >= len(word) && string(d.buf[d.pos:d.pos+len(word)]) == word {
		d.pos += len(word)
	} else {
		for i := range len(word) {
			c, err := d.next()
			if err != nil {
				return dst, err
			}
			if c != word[i] {
				return dst, d.malformed("invalid JSON")
			}
		}
	}
	if keep {
		dst = append(dst, word...)
	}
	return dst, nil
}

func (d *decoder) colon() error {
	c, err := d.peek()
	if err != nil {
		return err
	}
	if c != ':' {
		return d.malformed("invalid JSON")
	}
	d.pos++
	return nil
}

func (d *decoder) expect(want byte) error {
	c, err := d.peek()
	if err != nil {
		return err
	}
	if c != want {
		return d.malformed("unexpected token")
	}
	d.pos++
	return nil
}

func (d *decoder) peek() (byte, error) {
	if d.pos < d.end {
		if c := d.buf[d.pos]; c > ' ' {
			return c, nil
		}
	}
	return d.peekPastSpace()
}

func (d *decoder) peekPastSpace() (byte, error) {
	for {
		if err := d.more(); err != nil {
			return 0, err
		}
		switch c := d.buf[d.pos]; c {
		case ' ', '\t', '\n', '\r':
			d.pos++
		default:
			return c, nil
		}
	}
}

func (d *decoder) next() (byte, error) {
	if d.pos < d.end {
		c := d.buf[d.pos]
		d.pos++
		return c, nil
	}
	if err := d.more(); err != nil {
		return 0, err
	}
	return d.next()
}

func (d *decoder) offset() int64 {
	return d.off + int64(d.pos)
}

func (d *decoder) limitTo(limit int64, err error) {
	d.limit, d.limitErr = limit, err
	d.end = d.n
	if room := limit - d.off; room < int64(d.n) {
		d.end = int(max(room, int64(d.pos)))
	}
}

func (d *decoder) more() error {
	if d.pos < d.end {
		return nil
	}
	if d.end < d.n {
		return d.limitErr
	}
	if d.err != nil {
		return d.err
	}
	d.off += int64(d.n)
	d.pos, d.end, d.n = 0, 0, 0
	room := d.limit - d.off
	if room <= 0 {
		return d.probe()
	}
	for range maxEmptyRead {
		k, err := d.r.Read(d.buf[:min(int64(len(d.buf)), room)])
		d.n, d.end = k, k
		if err != nil {
			d.err = d.readError(err)
		}
		if k > 0 {
			return nil
		}
		if d.err != nil {
			return d.err
		}
	}
	d.err = d.readError(io.ErrNoProgress)
	return d.err
}

func (d *decoder) probe() error {
	var b [1]byte
	k, err := d.r.Read(b[:])
	switch {
	case k > 0:
		d.err = d.limitErr
	case err != nil:
		d.err = d.readError(err)
	default:
		return d.readError(io.ErrNoProgress)
	}
	return d.err
}

func (d *decoder) readError(err error) error {
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	return fmt.Errorf("stateread: read state at byte %d: %w", d.off+int64(d.n), err)
}
