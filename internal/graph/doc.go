// Package graph is the pure dependency resolution algorithm of stackorder.
// Given the graph of one repository at one commit and the paths a change
// touched, it computes the affected stacks, why each one is affected, the
// waves they apply in and the GitHub Actions matrix that runs them. It does
// no I/O and no logging; inputs are api/v1 values and so are outputs.
//
// # Edge direction
//
// Every edge points from the dependent node to the node it depends on, as in
// api/v1. An edge From A To B means A depends on B (depends_on), A reads the
// state of B (reads_state) or A uses module B (uses_module). B therefore
// applies before A, and a change to B propagates to A. Dependents are found
// by following edges backwards, dependencies by following them forwards, and
// for every ordering edge between two scheduled stacks
//
//	wave(edge.To) < wave(edge.From)
//
// # Resolution
//
// Resolve runs the five steps of the design document:
//
//  1. Directly changed stacks. Each changed path that survives the ignore
//     globs belongs to the deepest non-external stack directory enclosing it,
//     so a change in a nested stack never affects the stack around it. Every
//     workspace of that directory is affected. Paths with a ".terraform"
//     segment are ignored.
//  2. Module-affected stacks. Each changed path also belongs to the deepest
//     local module directory enclosing it; every stack that reaches such a
//     module over uses_module edges, through any number of nested modules,
//     is affected. Git and registry modules have no directory in the
//     repository and never match: their consumers change only when they bump
//     the pinned ref, which is a change to the consumer itself.
//  3. Propagation. When propagate.dependents is enabled, every stack with a
//     depends_on or reads_state edge to an affected stack is affected,
//     transitively. External stacks never propagate.
//  4. Ordering. The affected stacks are layered by longest path over their
//     depends_on and reads_state edges; edges to unaffected or external
//     stacks are dropped. A cycle fails the resolution with ErrCycle.
//  5. Cross-repo. External stacks with an ordering edge to a scheduled stack
//     are listed in ResolveResponse.External and never scheduled.
//
// A local stack whose key path or Path is not a canonical repository relative
// directory, such as "../x", "./x" or "/x", is never scheduled and is named in
// a warning, so no matrix entry points outside the checkout.
//
// A non-empty Input.Requested narrows the scheduled set to the requested
// stacks. Requested stacks that are affected keep their reasons; the others
// are added with the reason "requested". The relative order of two scheduled
// stacks is preserved even when the path between them runs through an
// affected stack that was not requested.
//
// # Reasons and Via
//
// Reasons are deduplicated and always ordered changed, module, reads_state,
// dependent, requested. Via is the sorted set of node keys the change reached
// the stack through: the affected modules the stack reaches over uses_module
// edges, and the affected stacks it has a depends_on or reads_state edge to.
//
// # Environments
//
// A stack runs under the environment its .stackorder.yaml names, else the
// longest matching prefix in the environments map of Input.Config, else
// Stack.Environment, else v1.DefaultEnvironment with a warning. The
// configuration the caller passes therefore decides, not the environment
// the runner computed from the change's own stackorder.yaml.
//
// # Cycles
//
// A cycle is spelled as the keys along it in edge direction, starting and
// ending at its smallest key: [a b c a] means a depends on b, b on c and c on
// a, and [a a] is a self-loop. One cycle is reported per strongly connected
// component, and cycles are sorted.
//
// # Determinism
//
// Every output is sorted: stacks by key within a wave, waves in order, and
// keys, warnings, cycles and external dependents lexicographically. The same
// graph and input always produce byte-identical JSON and DOT.
package graph
