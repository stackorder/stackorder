// Package scan turns a repository checkout into a dependency graph. It
// discovers stacks and their S3 backends, follows module sources through
// nested local modules, infers reads_state edges from terraform_remote_state
// data sources, applies each stack's depends_on list, and reports which paths
// changed between two git revisions.
package scan
