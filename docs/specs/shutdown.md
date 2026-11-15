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
- **Notices are arithmetic, not scheduling.** `ParseNotices` turns the operator's lead times (`24h,1h,15m,5m` by default) into absolute instants, drops the ones already past and sorts what remains, and reports whether any of them had passed. A schedule written inside a lead time has already missed a warning users were promised, so `schedule` sends one notice immediately; `--notify-now` additionally makes `fleet shutdown now` announce at once even when future leads remain. The list is capped at `MaxNotices` (8), and that bound is what makes a cancel without a readable state file complete: it finds the notice units by scanning up to exactly that many.
- **New logins stop before the poweroff, on their own timer.** `rootfiles-shutdown-block.timer` fires at `NologinLead` (5 minutes) before the instant and runs the hidden `shutdown block`, whatever lead times `--notice` holds: a `--notice 1h` schedule on a two-hour window would otherwise never close logins. A schedule created or announced *inside* that window blocks immediately instead, because its block timer would be set in the past and `Persistent=false` means it would never fire. `block` and `notify` write `/run/nologin` only in running `tenant-*` containers whose `/etc/pam.d/sshd` loads `pam_nologin`. They never write the host file, so non-root SSH operators can still reach the host to cancel. A host file carrying this feature's marker from an older version is removed during cancellation; foreign host files remain untouched. The file carries `rootfiles-shutdown: <id>`, and only a file carrying this feature's marker is ever removed or rewritten, so a `/run/nologin` another tool wrote is reported and left alone by both the block and the cancel. In a tenant, a file that exists but cannot be read is not an absent one: existence is probed separately and an unknown state is left in place, because `cat >` would otherwise truncate a login block this feature did not write.
- **Delivery is best effort; the schedule is not.** A container without `wall`, a failing `docker exec` or a missing `docker` is a warning on stderr and in the journal, never an error: one broken tenant must not block the notice reaching the others, and must not fail a schedule that is already in force.
- **A message never reaches a shell as an argument.** `--message` is sanitized to printable single-line text (`SanitizeMessage`, at most 300 characters) and stored in the state file; notices read it from there. Nothing renders it into a unit file. The fleet controller passes it with `fleet.ShellQuote`.
- **Cancel removes exactly what this feature created, and says when it cannot.** Units, the state file, the tenant `/run/nologin` files it wrote and - as legacy cleanup - a host `/run/nologin` carrying this schedule's marker, then a cancellation notice. A timer that will not stop, a unit that will not be removed and a timer whose state systemd will not confirm are errors, not warnings: systemd can fire a unit whose files are gone, so `is-active` must answer inactive or not-found before the files go, and an unreachable manager or a missing client leaves the state file in place so the schedule is still on record to be cancelled again. Running `cancel` twice is a no-op, and `Load` treats a missing state file as "nothing scheduled", not an error. When the state file cannot be read at all, `cancel` is still the recovery path its error names: it stops the poweroff, block and every notice timer the `MaxNotices` cap allows, removes a `/run/nologin` carrying this feature's marker for any id, removes the state file, and reports what it did.
- **A failure before the first write says so.** `ScheduleRun` wraps every refusal that happens before it writes a unit or the state file in `ErrUntouched` ("nothing was changed"), so a schedule already recorded on that host is known to be still in force. The fleet rollback reads that and leaves such a host out: cancelling it would remove a stop the operator never replaced.
- **An expired record is leftovers.** `Expired` treats a stored instant that is not in the future as no schedule: `status`, `shutdown status` and the fleet's read of it omit it, and the next `schedule` removes its units and record without asking for `--replace` and without telling sessions a live stop was cancelled.
- **The status field is additive.** `rootfiles status -o json` carries `scheduled_shutdown` only when a schedule exists; every other field, and the read-only sudoers command bytes, are unchanged. `rootfiles shutdown status -o json` reports `scheduled_shutdown`, `next_notice`, `logins_blocked` (true if at least one checked tenant is blocked, false if none are blocked, null if inspection is incomplete), `tenant_logins` (checked, blocked and unknown counts), `legacy_host_block` and `units_installed`. Status inspects only running tenant containers locally; inspection failures remain unknown, never unblocked.
- **Mutating commands take root and the lock.** `shutdown schedule`, `shutdown notify`, `shutdown block` and `shutdown cancel` are in `mutatingCommands`; `notify` and `block` are hidden from help and are what the timers run. Every write goes through `exec.Runner`, so `--dry-run` writes nothing and a backup session covers overwritten files.

Units rendered (all under `/etc/systemd/system`):

| Unit | Runs |
|---|---|
| `rootfiles-shutdown.service` | `/bin/sh -c 'systemctl poweroff'`, or `systemctl reboot` with `--reboot` |
| `rootfiles-shutdown.timer` | the instant, `Persistent=false`, `AccuracySec=1s` |
| `rootfiles-shutdown-block.service` | `<rootfiles> shutdown block` |
| `rootfiles-shutdown-block.timer` | `NologinLead` before the instant, `Persistent=false`, `AccuracySec=1s` |
| `rootfiles-shutdown-notice-<n>.service` | `<rootfiles> shutdown notify --notice '<lead>'` |
| `rootfiles-shutdown-notice-<n>.timer` | that notice's instant, `Persistent=false`, `AccuracySec=1s` |

`AccuracySec=1s` on every timer is a guardrail, not a detail: systemd's one-minute default lets each host pick its own second inside that window, which reintroduces exactly the drift the shared instant removes.

## Safety

- The poweroff is never reachable from a test: unit text is golden-compared, and `tests/scenarios/fleet.sh` runs the feature against containers with no systemd and a stub `systemctl`, so even `systemctl poweroff` is a no-op there.
- `Persistent=false` and the required offset are the two guardrails against a host stopping at the wrong moment; each has a test that fails if it is dropped.
- Cancelling is the one path that must always be able to run, so it continues past failures on the controller ([fleet](fleet.md)) and is idempotent on the host.

## Tests that pin it

- `internal/shutdown/shutdown_test.go`: RFC 3339 with an offset, the 1-minute/30-day window at both ends, notice skipping, ordering and the missed-lead report, the `MaxNotices` cap at exactly the maximum and one past it, message sanitizing and its limit.
- `internal/shutdown/units_test.go`: golden service and timer text, the UTC `OnCalendar`, `Persistent=false` and `AccuracySec=1s` on every timer, the login-block pair at `NologinLead`, the stop order (poweroff first) and enable order (poweroff last), reboot wording, zone independence, notice numbering, notice and cancellation text, and the `/run/nologin` marker.
- `internal/shutdown/notice_test.go`: a real schedule writing units, state and `systemctl` calls without ever running the action; `--replace` removing the shorter schedule's leftover notice units without announcing a cancellation; the immediate notice inside every lead, after a missed lead and with `--notify-now`; an expired record cleaned without `--replace`; a dry run creating nothing; cancel removing exactly its own files and being a no-op the second time; cancel failing and keeping the record when a timer stays active or a unit cannot be removed; cancel recovering an unreadable state file; host `/run/nologin` remaining untouched by the block, legacy marked host files removed on cancel, and foreign files surviving on the host and in a tenant; tenant broadcast with a fake `docker` and `wall` on a private `PATH`, where a failing container is one warning; the login block on its own timer whatever the notice list, the same block run immediately for a schedule created inside the five-minute window, a tenant file that exists but cannot be inspected being left in place, cancel failing when `is-active` will not answer and keeping the record, recovery stopping and removing every notice unit the cap allows, and only in containers whose PAM stack honours it.
- `internal/cli/shutdown_test.go`: every `--at` refusal naming the field and the form, nothing written on a refusal, a real schedule visible through `shutdown status` (text and JSON), a second schedule needing `--replace`, cancel idempotency, the hidden `notify`, and `status -o json` carrying `scheduled_shutdown` only when a schedule exists, with the status tests on a private `PATH` and stubbed account/cloudflared seams.
