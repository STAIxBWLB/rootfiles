---
id: shutdown
type: module
title: Scheduled shutdown with advance notice
status: active
parent: architecture
paths:
  - internal/shutdown/**
  - internal/cli/shutdown.go
---

# Scheduled shutdown with advance notice

History: specified in #134, built in the same cycle.

## Purpose

- One host stops at one absolute UTC instant whether or not anyone is logged in, after telling the people using it in advance.
- The fleet controller gives every selected host the same instant ([fleet](fleet.md)), so simultaneity comes from the shared instant and never from how long SSH took.
- Subjects who log in through sshpiper never see a host `wall`; notices therefore reach tenant containers too, through `docker exec <container> wall`.

## Contracts and invariants

- **The instant is absolute.** `ParseAt` accepts RFC 3339 only with an explicit offset (`Z` or `+hh:mm`) and returns UTC truncated to the second. A value without an offset is refused, because a host's own time zone must never move the moment. The window is 1 minute to 30 days ahead (`MinLead`, `MaxLead`).
- **A late boot never acts.** Every timer sets `Persistent=false`, so a host that was down at the instant does not power off when it comes back. `Units` renders `OnCalendar=<YYYY-MM-DD HH:MM:SS> UTC` for the main timer and each notice timer; the host's zone is never consulted.
- **Only one schedule exists per host.** The state file is `/var/lib/rootfiles/shutdown.json` (`state.Dir()`, 0644, written through the runner and renamed into place): `id`, `at`, `action`, `message`, `notices[]` (`lead`, `at`), `created_by`, `created_at`. A second `schedule` names the existing one and needs `--replace`, which removes the old units, login block and state first.
- **Notices are arithmetic, not scheduling.** `ParseNotices` turns the operator's lead times (`24h,1h,15m,5m` by default) into absolute instants, drops the ones already past and sorts what remains. When none is left, `schedule` sends one notice immediately; `--notify-now` additionally makes `fleet shutdown now` announce at once even when future leads remain.
- **New logins stop before the poweroff.** From `NologinLead` (5 minutes) before the instant, `notify` writes `/run/nologin` on the host and in every running `tenant-*` container whose `/etc/pam.d/sshd` loads `pam_nologin`. The file carries `rootfiles-shutdown: <id>`, and only a file carrying the current schedule's marker is ever removed, so a `/run/nologin` another tool wrote survives both cancel and the next schedule.
- **Delivery is best effort; the schedule is not.** A container without `wall`, a failing `docker exec` or a missing `docker` is a warning on stderr and in the journal, never an error: one broken tenant must not block the notice reaching the others, and must not fail a schedule that is already in force.
- **A message never reaches a shell as an argument.** `--message` is sanitized to printable single-line text (`SanitizeMessage`, at most 300 characters) and stored in the state file; notices read it from there. Nothing renders it into a unit file. The fleet controller passes it with `fleet.ShellQuote`.
- **Cancel removes exactly what this feature created.** Units, the state file and a `/run/nologin` carrying this schedule's marker, then a cancellation notice. Running `cancel` twice is a no-op, and `Load` treats a missing state file as "nothing scheduled", not an error.
- **The status field is additive.** `rootfiles status -o json` carries `scheduled_shutdown` only when a schedule exists; every other field, and the read-only sudoers command bytes, are unchanged. `rootfiles shutdown status -o json` reports `scheduled_shutdown`, `next_notice`, `logins_blocked` and `units_installed`.
- **Mutating commands take root and the lock.** `shutdown schedule`, `shutdown notify` and `shutdown cancel` are in `mutatingCommands`; `notify` is hidden from help and is what the notice timers run. Every write goes through `exec.Runner`, so `--dry-run` writes nothing and a backup session covers overwritten files.

Units rendered (all under `/etc/systemd/system`):

| Unit | Runs |
|---|---|
| `rootfiles-shutdown.service` | `/bin/sh -c 'systemctl poweroff'`, or `systemctl reboot` with `--reboot` |
| `rootfiles-shutdown.timer` | the instant, `Persistent=false` |
| `rootfiles-shutdown-notice-<n>.service` | `<rootfiles> shutdown notify --notice '<lead>'` |
| `rootfiles-shutdown-notice-<n>.timer` | that notice's instant, `Persistent=false` |

## Safety

- The poweroff is never reachable from a test: unit text is golden-compared, and `tests/scenarios/fleet.sh` runs the feature against containers with no systemd and a stub `systemctl`, so even `systemctl poweroff` is a no-op there.
- `Persistent=false` and the required offset are the two guardrails against a host stopping at the wrong moment; each has a test that fails if it is dropped.
- Cancelling is the one path that must always be able to run, so it continues past failures on the controller ([fleet](fleet.md)) and is idempotent on the host.

## Tests that pin it

- `internal/shutdown/shutdown_test.go`: RFC 3339 with an offset, the 1-minute/30-day window at both ends, notice skipping and ordering, message sanitizing and its limit.
- `internal/shutdown/units_test.go`: golden service and timer text, the UTC `OnCalendar`, `Persistent=false` on every timer, reboot wording, zone independence, notice numbering, notice and cancellation text, and the `/run/nologin` marker.
- `internal/shutdown/notice_test.go`: a real schedule writing units, state and `systemctl` calls without ever running the action; `--replace` removing the shorter schedule's leftover notice units; the immediate notice inside every lead and with `--notify-now`; a dry run creating nothing; cancel removing exactly its own files and being a no-op the second time; a foreign `/run/nologin` surviving; tenant broadcast with a fake `docker` and `wall` on a private `PATH`, where a failing container is one warning; the login block inside the 5-minute window and only in containers whose PAM stack honours it.
- `internal/cli/shutdown_test.go`: every `--at` refusal naming the field and the form, nothing written on a refusal, a real schedule visible through `shutdown status` (text and JSON), a second schedule needing `--replace`, cancel idempotency, the hidden `notify`, and `status -o json` carrying `scheduled_shutdown` only when a schedule exists.
