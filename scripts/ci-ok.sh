#!/usr/bin/env bash
# Gate every job, allowing expensive-job skips only when decision requests them.
set -euo pipefail

check_results() {
  jq -e '
    .decision.result == "success" and
    .["release-notes"].result == "success" and
    (.decision.outputs.run_full == "true" or .decision.outputs.run_full == "false") and
    (.decision.outputs.run_full as $full |
      to_entries | all(.[];
        if .key == "decision" or .key == "release-notes" then
          .value.result == "success"
        elif $full == "true" then .value.result == "success"
        else .value.result == "skipped"
        end))
  ' >/dev/null
}

if [ "${1:-}" = "--self-test" ]; then
  full='{"decision":{"result":"success","outputs":{"run_full":"true"}},"release-notes":{"result":"success"},"unit":{"result":"success"}}'
  skipped=$(jq '.decision.outputs.run_full = "false" | .unit.result = "skipped"' <<<"$full")
  check_results <<<"$full"
  check_results <<<"$skipped"
  for fixture in "$full" "$skipped"; do
    for job in decision release-notes unit; do
      for result in failure cancelled skipped; do
        # Only decision-authorized expensive-job skips may pass.
        if [ "$fixture" = "$skipped" ] && [ "$job" = unit ] && [ "$result" = skipped ]; then
          continue
        fi
        if jq --arg job "$job" --arg result "$result" '.[$job].result = $result' <<<"$fixture" | check_results; then
          echo "FAIL: accepted $job=$result" >&2
          exit 1
        fi
      done
    done
  done
  for output in '' unknown; do
    if jq --arg output "$output" '.decision.outputs.run_full = $output' <<<"$skipped" | check_results; then
      echo "FAIL: accepted unknown decision output" >&2
      exit 1
    fi
  done
  if check_results <<<'{}'; then
    echo "FAIL: accepted missing jobs" >&2
    exit 1
  fi
  echo "ci-ok self-test: ok"
  exit 0
fi

if ! check_results <<<"${NEEDS_JSON:?NEEDS_JSON must contain the workflow needs results}"; then
  echo "::error::CI jobs failed, were cancelled, or skipped without a successful run_full=false decision. Inspect the gating jobs."
  exit 1
fi
