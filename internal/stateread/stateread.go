// Package stateread reads the root module outputs of a Terraform or OpenTofu
// state object in S3 so that the server can share them with the stacks a
// producer lists as readers. Each grant ties a bucket, or a key prefix in
// it, to the one repository whose state it holds, or to every repository
// with "*", and every read is made for the producer's repository, so the
// location that a repository's apply and drift jobs report can make the
// server read only what the operator granted that repository. It decodes
// only the outputs block, stops reading as soon as it has it, and never
// stores, caches or logs a state object or any value in it. Errors name
// byte offsets, never state content.
package stateread

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	// MaxObjectBytes is the most of a state object Read scans for its
	// outputs before it gives up with ErrTooLarge. Terraform and OpenTofu
	// write the outputs before the resources, so only a state written in
	// another order comes near it.
	MaxObjectBytes = 64 << 20

	// MaxOutputsBytes caps the outputs block, and any single string or
	// number scanned on the way to it, at 4 MiB. Each decoded output also
	// counts its name, value and type plus a fixed cost for its map entry
	// against the cap, so that many small outputs cannot hold more memory
	// than it.
	MaxOutputsBytes = 4 << 20

	// Timeout bounds one Read from the moment it has a slot.
	Timeout = 30 * time.Second

	// MaxWait bounds how long a Read waits for a slot, so that waiting and
	// Timeout together stay within a minute, the idle timeout of the load
	// balancer in the AWS deployment, and well within the server's write
	// timeout.
	MaxWait = 20 * time.Second

	// MaxConcurrent is how many reads one Reader runs at a time; further
	// reads wait up to MaxWait for a slot.
	MaxConcurrent = 4
)

var (
	// ErrNotFound is returned when the state object or its bucket does not
	// exist.
	ErrNotFound = errors.New("stateread: state object not found")

	// ErrAccessDenied is returned when S3 refuses the server's credentials.
	ErrAccessDenied = errors.New("stateread: access to the state object denied")

	// ErrEncrypted is returned for a state object encrypted on the client
	// side by OpenTofu, which is never decrypted.
	ErrEncrypted = errors.New("stateread: state is encrypted")

	// ErrUnsupported is returned for a state format other than version 4.
	ErrUnsupported = errors.New("stateread: unsupported state version")

	// ErrTooLarge is returned when a state object exceeds MaxObjectBytes
	// before its outputs, or its outputs exceed MaxOutputsBytes.
	ErrTooLarge = errors.New("stateread: state too large")

	// ErrNotAllowed is returned, before any request to S3, for a location
	// that no grant allows the producer's repository.
	ErrNotAllowed = errors.New("stateread: state location not allowed")

	// ErrUnreadable is returned for an unusable location and for permanent
	// S3 errors other than ErrNotFound and ErrAccessDenied.
	ErrUnreadable = errors.New("stateread: state object unreadable")

	// ErrMalformed is returned for a state object that is not valid state
	// JSON; its message names a byte offset only.
	ErrMalformed = errors.New("stateread: malformed state")
)

// Location names a state object: the bucket, the object key and the
// bucket's region, as the s3 backend of the stack configures them.
type Location struct {
	Bucket string
	Key    string
	Region string
}

// State is what Read keeps of a state object: its identity and its root
// module outputs. Decode keeps at most MaxOutputsBytes of outputs and 256
// bytes each of lineage and Terraform version.
type State struct {
	Serial           uint64
	Lineage          string
	TerraformVersion string
	Outputs          map[string]Output
}

// Output is one root module output: its value and cty type as compact
// JSON, and whether the configuration marks it sensitive.
type Output struct {
	Value     json.RawMessage
	Type      json.RawMessage
	Sensitive bool
}
