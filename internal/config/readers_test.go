package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReaderAllowed(t *testing.T) {
	const producer = "acme/infra"
	tests := []struct {
		name       string
		readers    []string
		readerRepo string
		readerKey  string
		want       bool
	}{
		{name: "exact key", readers: []string{"stacks/prod/eks"}, readerRepo: producer, readerKey: "stacks/prod/eks", want: true},
		{name: "an exact key is not a prefix", readers: []string{"stacks/prod/eks"}, readerRepo: producer, readerKey: "stacks/prod/eks/addons"},
		{name: "an exact key names no instance", readers: []string{"stacks/prod/eks"}, readerRepo: producer, readerKey: "stacks/prod/eks:blue"},
		{name: "star within a segment", readers: []string{"stacks/prod/*"}, readerRepo: producer, readerKey: "stacks/prod/eks", want: true},
		{name: "star does not cross a slash", readers: []string{"stacks/*"}, readerRepo: producer, readerKey: "stacks/prod/eks"},
		{name: "star crosses a colon", readers: []string{"stacks/prod/*"}, readerRepo: producer, readerKey: "stacks/prod/apps:blue", want: true},
		{name: "double star crosses slashes", readers: []string{"stacks/**"}, readerRepo: producer, readerKey: "stacks/prod/apps:blue", want: true},
		{name: "double star crosses slashes only as a whole segment", readers: []string{"stacks/**:blue"}, readerRepo: producer, readerKey: "stacks/prod/apps:blue"},
		{name: "double star within a segment acts as a star", readers: []string{"stacks/**:blue"}, readerRepo: producer, readerKey: "stacks/apps:blue", want: true},
		{name: "every blue instance under a directory", readers: []string{"stacks/**/*:blue"}, readerRepo: producer, readerKey: "stacks/prod/apps:blue", want: true},
		{name: "double star alone matches every key", readers: []string{"**"}, readerRepo: producer, readerKey: "infra/dns", want: true},
		{name: "path:* matches every instance", readers: []string{"stacks/prod/apps:*"}, readerRepo: producer, readerKey: "stacks/prod/apps:blue", want: true},
		{name: "path:* does not match the unnamed instance", readers: []string{"stacks/prod/apps:*"}, readerRepo: producer, readerKey: "stacks/prod/apps"},
		{name: "one matching entry is enough", readers: []string{"stacks/prod/dns", "stacks/prod/eks"}, readerRepo: producer, readerKey: "stacks/prod/eks", want: true},
		{name: "another repository", readers: []string{"acme/platform//clusters/**"}, readerRepo: "acme/platform", readerKey: "clusters/eu/eks", want: true},
		{name: "another repository in another letter case", readers: []string{"Acme/Platform//clusters/**"}, readerRepo: "acme/platform", readerKey: "clusters/eu/eks", want: true},
		{name: "the producer's repository in another letter case", readers: []string{"stacks/**"}, readerRepo: "ACME/Infra", readerKey: "stacks/prod/eks", want: true},
		{name: "a qualified entry naming the producer", readers: []string{"acme/infra//stacks/**"}, readerRepo: producer, readerKey: "stacks/prod/eks", want: true},
		{name: "a qualified entry does not match the producer's repository", readers: []string{"acme/platform//stacks/**"}, readerRepo: producer, readerKey: "stacks/prod/eks"},
		{name: "a same-repository entry does not match another repository", readers: []string{"stacks/**"}, readerRepo: "acme/platform", readerKey: "stacks/prod/eks"},
		{name: "a qualified entry does not match a third repository", readers: []string{"acme/platform//**"}, readerRepo: "acme/apps", readerKey: "stacks/prod/eks"},
		{name: "empty list", readers: []string{}, readerRepo: producer, readerKey: "stacks/prod/eks"},
		{name: "nil list", readerRepo: producer, readerKey: "stacks/prod/eks"},
		{name: "an invalid glob never matches", readers: []string{"stacks/["}, readerRepo: producer, readerKey: "stacks/["},
		{name: "a repository pattern never matches", readers: []string{"acme/*//stacks/**"}, readerRepo: "acme/*", readerKey: "stacks/prod/eks"},
		{name: "an absolute pattern never matches", readers: []string{"/stacks/prod/eks"}, readerRepo: producer, readerKey: "/stacks/prod/eks"},
		{name: "an empty entry never matches", readers: []string{""}, readerRepo: producer, readerKey: ""},
		{name: "a leading ./ never matches", readers: []string{"./stacks/prod/eks"}, readerRepo: producer, readerKey: "stacks/prod/eks"},
		{name: "a trailing slash never matches", readers: []string{"stacks/prod/eks/"}, readerRepo: producer, readerKey: "stacks/prod/eks"},
		{name: "a double slash in the pattern never matches", readers: []string{"acme/platform//clusters//eu"}, readerRepo: "acme/platform", readerKey: "clusters/eu"},
		{name: "a ./ in a qualified pattern never matches", readers: []string{"acme/platform//./clusters/**"}, readerRepo: "acme/platform", readerKey: "clusters/eu/eks"},
		{name: "a .. segment never matches", readers: []string{"stacks/../eks"}, readerRepo: producer, readerKey: "stacks/../eks"},
		{name: "a pattern above the repository never matches", readers: []string{"../stacks/**"}, readerRepo: producer, readerKey: "../stacks/prod/eks"},
		{name: "a surrounding space never matches", readers: []string{" stacks/prod/eks"}, readerRepo: producer, readerKey: " stacks/prod/eks"},
		{name: "the default instance never matches", readers: []string{"stacks/prod/eks:default"}, readerRepo: producer, readerKey: "stacks/prod/eks"},
		{name: "a repository without an owner never matches", readers: []string{"infra//stacks/**"}, readerRepo: "infra", readerKey: "stacks/prod/eks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ReaderAllowed(tt.readers, producer, tt.readerRepo, tt.readerKey))
		})
	}
}
