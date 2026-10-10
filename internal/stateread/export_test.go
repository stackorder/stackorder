package stateread

import "io"

// DecodeWithLimits is Decode with the object and outputs caps lowered for
// tests.
func DecodeWithLimits(r io.Reader, maxObject, maxOutputs int64) (*State, error) {
	return decode(r, maxObject, maxOutputs)
}
