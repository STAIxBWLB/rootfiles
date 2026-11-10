# Review instructions

Read by the required OCR delegation reviewer (Claude Code, Codex, or Kimi Code),
additional `/codex:review` or `/code-review` passes, and human reviewers alike.

## Passes

Run these passes and tag every finding with its pass:

- Bugs: logic errors, broken edge cases, regressions.
- Error messages: a new or changed error, refusal or failure message a person or agent sees names the cause (what failed and why) and the next step (the command, setting or target that fixes it). When secrets, ids or paths must stay out, it names the cause class and how to check it. A message missing either is a finding; Important when it is the only signal of a blocked action (a CLI exit, a refused tool call), otherwise a nit.
- Security: injection, auth gaps, secrets or PII in logs and diffs.
- Compliance: the change matches the issue spec and the approved plan.
  Workflow changes (`.github/workflows/`) are also checked for job timeouts, concurrency, and
  one build per commit.
- Instruction prose (changes to AGENTS.md, REVIEW.md, SKILL.md, or prompts): for each "do not X", would
  "do Y" alone keep the force and the boundary? Keep it for safety, permission, and contract
  boundaries. Would the principle generalize better without an example? Keep examples that fix
  a format or a high-failure behavior. Is a chain of cases standing in for a judgment? Does a new
  directive name the failure it prevents? Nits unless runtime behavior changes.

## Repo focus

- Host safety (AGENTS.md Conventions; README Safety): file writes and removals go through
  `rc.Runner`, never `os.*` directly, so `--dry-run` and backups apply; user data is never
  `rm -rf`'d; SSH changes keep the lockout guard. Unit tests stay off the host by overriding path
  vars and stubbing commands with `fakeBin`.
- `Check()` has no side effects, and `Apply()` acts only on what `Check()` reports, stays
  idempotent, and returns `Changed` only for real work.
- A new native-host mutating command is added to `mutatingCommands` in `internal/cli/runtime.go` so it gets
  the root and lock preflight. Fleet controller rollouts stay unprivileged on the operator machine,
  enforce explicit selection/confirmation, and invoke remote native mutations with their own lock.
- Secrets such as the tunnel token and user passwords stay out of the command lines rootfiles
  spawns, its logs, and `config show` output (README: the token is masked and kept in a root-only
  env file; passwords reach `chpasswd` on stdin). The CLI itself still accepts them as input
  (`--password`, `--tunnel-token`, `tunnel setup [TOKEN]`); a change that echoes or forwards that
  input is Important.

## Findings

Each finding carries its pass, severity, evidence (`file:line` or a reproduction), and provenance:
introduced by this change, pre-existing, or indeterminate. Pre-existing findings go to a follow-up
issue instead of widening the change. Report what was reviewed and what was not reached.

## Re-review

Hand the reviewer the diff, the spec, and this file, and let it read the rest of the repo (AGENTS.md
included). Do not hand it fix-status claims, earlier dispositions, or do-not-reflag notes. Judge
recurrence by the violated invariant, not by wording.

## What Important means here

Reserve Important for findings that break behavior, leak data, or breach a policy.
Style and naming are nits.

## Cap the nits

Report at most 5 nits per review; summarize the rest as a count.

## Do not report

- Generated paths: `go.sum`.
  Lockfiles are skipped for style only: a lockfile change without a matching manifest change stays
  in the Security pass.
- Anything CI already enforces: `gofmt`, `go vet`, `go mod tidy` drift, `govulncheck`,
  `go test ./... -race` (including `TestRegistryDefaultOrderSync`), the build and `--help` smoke
  check, and the Docker integration, per-module, and scenario suites in
  `.github/workflows/test.yaml`. That workflow runs only when code, test, or build paths change.

## Feedback into AGENTS.md

When the same finding appears twice, the correction goes into `AGENTS.md` in the same PR.

---

Findings require evidence-based disposition. Merge gates and review routing follow the owner's
development lifecycle.
