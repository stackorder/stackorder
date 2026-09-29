# Exit codes

Every `stackorder` command exits with one of four codes. Scripts and hooks can rely on them; the reusable workflows do.

| Code | Name | Meaning |
| --- | --- | --- |
| `0` | success | The command did what it was asked. |
| `1` | error | Bad flags or configuration, a failed Terraform or OpenTofu command, a failed hook, a dependency cycle, or a server error that is not a refusal. |
| `2` | changes | A result rather than a failure: `affected` found affected stacks, or `drift` found drift. |
| `3` | refused | The server refused the request, or the CLI refused to apply because it could not confirm the apply (fail closed). |

## By command {#by-command}

| Command | `0` | `1` | `2` | `3` |
| --- | --- | --- | --- | --- |
| `resolve` | Resolved, with or without affected stacks, confirmed or `unconfirmed` | Scan or config error, cycle, server error | | Server refusal, such as `superseded` |
| `plan` | Plan succeeded, with or without changes, confirmed or `unconfirmed` | `init`, `plan`, `show` or a hook failed | | The server refused the result |
| `apply` | Applied and reported | Apply or `post-apply` hook failed, reporting failed, usage error (`--local` in Actions, no run id, no API key) | | Run or stack not `planned`, commit mismatch, re-plan differs from the recorded plan, no server in Actions, server unreachable, lock refused |
| `drift` | No drift | The drift check failed | Drift found | The server refused the result |
| `check` | Verdict recorded | Usage error, no run id, no server | | The server refused the verdict |
| `graph` | Graph printed | Scan or config error | | |
| `affected` | No stack affected | Scan or config error, cycle | At least one stack affected | |
| `unlock` | Every named lock released, or none was held | No server, no API key, unknown repository or stack | | The server refused the unlock |
| `version` | Always | `--format dot` | | |

## How server answers map to exit codes {#server-answers}

| Server answer | Exit code |
| --- | --- |
| 2xx | `0`, or `2` for `affected` and `drift` results |
| `403 forbidden`, `409 conflict`, `409 refused`, `409 superseded`, `423 locked` | `3` |
| `401 unauthorized`, `400 invalid`, `404 not_found`, `413` | `1` |
| Network error, timeout or 5xx after three retries | Unreachable: `resolve`, `plan` and `drift` continue with an `unconfirmed` result and exit as if the call had not been made; `apply` exits `3` |

See [API errors](/reference/api#errors) for the codes themselves.

## In workflows {#workflows}

The [`drift` action](/reference/actions#drift) records exit code 2 in its `exit-code` output and succeeds; any other non-zero code fails the step. The other actions fail their step on any non-zero exit code, so a plan with changes (`0`) passes and a refused apply (`3`) fails the job.

In a shell step:

```sh
set +e
stackorder affected --base origin/main
code=$?
set -e
case "$code" in
  0) echo "nothing to plan" ;;
  2) echo "stacks affected" ;;
  *) exit "$code" ;;
esac
```
