// Package tf wraps the terraform and tofu binaries for the stackorder CLI: it
// finds the tool, runs init, workspace selection, plan, show, apply and output
// with automation-safe flags, digests plan JSON into v1.PlanSummary, redacts
// secrets from text bound for the server, and runs repository hooks.
package tf
