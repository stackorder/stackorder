package stateread

import (
	"errors"
	"io"
	"time"
)

// DecodeWithLimits is Decode with the object and outputs caps lowered for
// tests.
func DecodeWithLimits(r io.Reader, maxObject, maxOutputs int64) (*State, error) {
	return decode(r, maxObject, maxOutputs)
}

// SkipValue checks one JSON value of r the way Decode checks the values it
// skips, strings and numbers up to maxOutputs bytes, and fails unless only
// whitespace follows it.
func SkipValue(r io.Reader, maxOutputs int64) error {
	d := newDecoder(r, MaxObjectBytes, maxOutputs)
	if _, err := d.value(nil, false); err != nil {
		return d.fail(err)
	}
	_, err := d.peek()
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return d.fail(err)
	}
	return d.malformed("data after the value")
}

// WithWait is an Option that sets how long a read waits for a slot in
// place of MaxWait.
func WithWait(wait time.Duration) Option {
	return func(o *options) { o.wait = wait }
}
