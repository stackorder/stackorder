// Package v1 defines the JSON types exchanged between the stackorder CLI, the
// stackorder server, the embedded web UI and external tooling.
//
// Everything in this package is a plain data type with JSON tags and no
// behaviour beyond small helpers. It is the only package shared by the runner
// side (cmd/stackorder) and the server side (cmd/stackorder-server), and it is
// importable from outside the module so automation can decode API responses.
//
// # Identities
//
// A stack is identified inside its repository by its Key: the repository
// relative directory path, followed by ":" and the workspace when the
// workspace is not "default". Across repositories the identity is
// "owner/repo//key".
//
//	stacks/prod/vpc
//	stacks/prod/vpc:blue
//	acme/network-infra//stacks/prod/tgw
//
// A module is identified by its Key, whose shape depends on its kind:
//
//	acme/infra//modules/vpc                     local module (owner/repo//path)
//	acme/modules//vpc@v1.2.0                    git module on the GitHub instance
//	gitlab.com/acme/modules//vpc@v1.2.0         git module on another host
//	registry:terraform-aws-modules/vpc/aws@5.1  registry module
//
// # Versioning
//
// Fields are only ever added. Consumers must ignore unknown fields.
package v1
