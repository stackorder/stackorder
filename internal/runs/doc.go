// Package runs is the run state machine of the stackorder server: it
// registers and resolves plan runs, records results, evaluates the apply
// gate, takes and releases orchestration locks, dispatches
// stackorder-run.yml wave by wave, supersedes stale plans, executes pull
// request comment commands, schedules drift checks, reconciles lost
// workflow_run webhooks, re-learns the App's installations and plans
// cross-repository dependents.
//
// # Runs
//
// A plan run belongs to one pull request head commit. The resolve job of
// stackorder-plan.yml creates it (CreateRun) and uploads the scanned graph
// (UploadGraph); each plan job reports its stack (RecordResult). A new head
// commit supersedes the plan runs of older commits. "stackorder plan" and
// check re-runs start further plan runs on the same commit, dispatched by
// the server; the stacks of every plan run of a head commit merge, newest
// row first, into the view the plan roll-up check, the sticky comment and
// the apply gate read.
//
// An apply run is started by "stackorder apply" (before_merge), by the
// merge (on_merge) or by an API key (manual, run by the CLI itself). It
// copies the plans of the head commit, takes the locks of its stacks, and
// is dispatched one wave at a time: wave n+1 when every stack of wave n is
// terminal and none failed. A failed stack blocks its transitive
// dependents; the run then fails, and its locks stay held unless it was an
// on_merge or manual run. Drift runs check one stack on the default branch
// head.
//
// # Consistency across workers and servers
//
// Every handler may run concurrently with others for the same run, on any
// server, and may be retried after a crash, so the package never relies on
// in-process state for correctness:
//
//   - State changes are single guarded statements: a stack row moves only
//     from the statuses a transition allows, a run's status only from the
//     statuses its target allows, and its wave only forward. Only the caller
//     whose guarded update succeeded performs the one-time effects of a
//     transition, such as the failure comment or releasing locks.
//   - A dispatch row is unique per (run, wave, environment, mode, chunk).
//     Only the caller that created it sends the workflow_dispatch call and
//     marks it sent; Reconcile resends dispatches left unsent by a crash,
//     and a duplicated workflow run is refused when its jobs try to bind.
//   - Find-or-create steps (the plan run of a commit, the drift run of an
//     hour, the apply of a merge, the execution of a comment) run in a
//     transaction holding an advisory lock on their identity; so does the
//     start of an apply, which a pull request runs one at a time.
//   - GitHub check runs and the sticky comment are rewritten from the
//     current database state while holding an advisory lock per pull
//     request (per run outside pull requests), in a transaction that also
//     records the ids of created check runs. The last writer therefore
//     always renders the newest state, and a check run is never created
//     twice.
//
// Re-delivered events and duplicated result posts find their work done and
// return nil. Timestamps stored by Postgres are compared with
// Config.Clock, which tests replace.
package runs
