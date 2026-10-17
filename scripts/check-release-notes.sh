#!/usr/bin/env bash
# Check release notes against the AGENTS.md "Release notes" format (format
# only, not wording):
#   - line 1 is a plain sentence, not a heading or blank
#   - headings are only ### New, ### Improved, ### Fixed, ### Action needed,
#     in that order, each at most once
#   - every included section holds only "- " bullets, at least one; from
#     PR_REFS_FROM on each bullet ends with its PR number, e.g. (#123)
#   - no 40-hex commit SHAs, no conventional-commit prefixes on bullets
#   - the last line is the compare link from the previous note's tag, or the
#     commits link for the first release
#
# Usage: scripts/check-release-notes.sh [file...]   (default: docs/releases/v*.md)
#        scripts/check-release-notes.sh --self-test
set -uo pipefail

REPO_URL="https://github.com/STAIxBWLB/rootfiles"
SECTIONS=("### New" "### Improved" "### Fixed" "### Action needed")
TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'
SHA_RE='(^|[^0-9a-fA-F])[0-9a-fA-F]{40}([^0-9a-fA-F]|$)'
PR_REF_RE='\(#[0-9]+(, #[0-9]+)*\)$'
# Earlier notes predate PR references and are kept as published.
PR_REFS_FROM=v0.11.0
PREFIX_RE='^[-*][[:space:]]+(feat|fix|docs|chore|refactor|test|ci|perf|build|style|revert)(\([^)]*\))?!?:'

# previous_tag FILE prints the tag of the note just before FILE by version.
previous_tag() {
  local dir tag
  dir=$(dirname "$1")
  tag=$(basename "$1" .md)
  { for f in "$dir"/v*.md; do [ -e "$f" ] && basename "$f" .md; done; echo "$tag"; } |
    grep -E "$TAG_RE" | sort -u -V | grep -B1 -x -- "$tag" | grep -vx -- "$tag" || true
}

check_file() {
  local f=$1 tag n=0 line last="" lastn=0 section=-1 idx i prev want errs=0 refs=0 open="" bullets=0
  err() { echo "$f:$1: $2"; errs=1; }
  # close_section reports a section heading that had no bullets under it.
  close_section() { [ -n "$open" ] && [ "$bullets" = 0 ] && err "$open" "section has no bullets"; open=""; }
  if [ ! -s "$f" ]; then
    echo "$f: missing or empty"
    return 1
  fi
  tag=$(basename "$f" .md)
  [[ $tag =~ $TAG_RE ]] || err 0 "file name is not vX.Y.Z.md"
  [ "$(printf '%s\n' "$PR_REFS_FROM" "$tag" | sort -V | head -1)" = "$PR_REFS_FROM" ] && refs=1
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    if [ "$n" = 1 ] && { [ -z "$line" ] || [[ $line == \#* ]]; }; then
      err 1 "first line must be a one-sentence summary, not a heading or blank"
    fi
    if [[ $line == '**Full changelog**:'* ]]; then
      close_section
    elif [ -n "$open" ] && [ -n "$line" ] && [[ $line != \#* ]]; then
      if [[ $line != "- "* ]]; then
        err "$n" "section text must be '- ' bullets"
      else
        bullets=$((bullets + 1))
        [ "$refs" = 1 ] && ! [[ $line =~ $PR_REF_RE ]] && err "$n" "bullet must end with its PR number, e.g. (#123)"
      fi
    fi
    if [[ $line == \#* ]]; then
      close_section
      open=$n
      bullets=0
      idx=-1
      for i in "${!SECTIONS[@]}"; do [ "$line" = "${SECTIONS[$i]}" ] && idx=$i; done
      if [ "$idx" -lt 0 ]; then
        err "$n" "heading '$line' is not one of: ${SECTIONS[*]}"
      elif [ "$idx" -le "$section" ]; then
        err "$n" "'$line' is repeated or out of order (New, Improved, Fixed, Action needed)"
      else
        section=$idx
      fi
    fi
    [[ $line =~ $SHA_RE ]] && err "$n" "contains a commit SHA"
    [[ $line =~ $PREFIX_RE ]] && err "$n" "starts with a conventional-commit prefix"
    if [ -n "$line" ]; then
      last=$line
      lastn=$n
    fi
  done <"$f"
  close_section
  prev=$(previous_tag "$f")
  if [ -n "$prev" ]; then
    want="**Full changelog**: $REPO_URL/compare/$prev...$tag"
  else
    want="**Full changelog**: $REPO_URL/commits/$tag"
  fi
  [ "$last" = "$want" ] || err "$lastn" "last line must be: $want"
  return "$errs"
}

self_test() {
  local dir fail=0
  dir=$(mktemp -d)
  trap 'rm -rf "$dir"' RETURN
  expect() { # $1 ok|fail, $2 file, $3 expected message fragment
    local out rc
    out=$(check_file "$2")
    rc=$?
    if [ "$1" = ok ] && [ "$rc" != 0 ]; then echo "FAIL: $2 should pass: $out"; fail=1; fi
    if [ "$1" = fail ] && { [ "$rc" = 0 ] || [[ $out != *"$3"* ]]; }; then echo "FAIL: $2 should fail with '$3': $out"; fail=1; fi
  }
  printf 'First release.\n\n### New\n- Something (#1)\n\n**Full changelog**: %s/commits/v1.0.0\n' "$REPO_URL" >"$dir/v1.0.0.md"
  printf 'Adds a thing.\n\n### New\n- A thing (#2)\n\n### Fixed\n- A bug (#3)\n\n**Full changelog**: %s/compare/v1.0.0...v1.1.0\n' "$REPO_URL" >"$dir/v1.1.0.md"
  expect ok "$dir/v1.0.0.md"
  expect ok "$dir/v1.1.0.md"
  mkdir "$dir/bad"
  cp "$dir/v1.0.0.md" "$dir/bad/v1.0.0.md"
  printf '### New\n- A thing (#2)\n\n**Full changelog**: %s/compare/v1.0.0...v1.1.0\n' "$REPO_URL" >"$dir/bad/v1.1.0.md"
  expect fail "$dir/bad/v1.1.0.md" ":1: first line"
  printf 'Fix.\n\n### Fixed\n- A bug in 0123456789abcdef0123456789abcdef01234567 (#3)\n\n**Full changelog**: %s/compare/v1.1.0...v1.2.0\n' "$REPO_URL" >"$dir/bad/v1.2.0.md"
  expect fail "$dir/bad/v1.2.0.md" ":4: contains a commit SHA"
  printf 'Fix.\n\n### Fixed\n- A bug (#3)\n\n**Full changelog**: %s/compare/v1.0.0...v1.3.0\n' "$REPO_URL" >"$dir/bad/v1.3.0.md"
  expect fail "$dir/bad/v1.3.0.md" "compare/v1.2.0...v1.3.0"
  printf 'Fix.\n\n### Fixed\n- A bug (#3)\n\n### New\n- A thing (#4)\n\n**Full changelog**: %s/compare/v1.3.0...v1.4.0\n' "$REPO_URL" >"$dir/bad/v1.4.0.md"
  expect fail "$dir/bad/v1.4.0.md" ":6: '### New' is repeated or out of order"
  printf 'Fix.\n\n## Changes\n- fix(cli): a bug (#3)\n\n**Full changelog**: %s/compare/v1.4.0...v1.5.0\n' "$REPO_URL" >"$dir/bad/v1.5.0.md"
  expect fail "$dir/bad/v1.5.0.md" ":3: heading '## Changes'"
  expect fail "$dir/bad/v1.5.0.md" ":4: starts with a conventional-commit prefix"
  printf 'Fix.\n\n### Fixed\n- A bug\nSome prose.\n\n### Action needed\n\n**Full changelog**: %s/compare/v1.5.0...v1.6.0\n' "$REPO_URL" >"$dir/bad/v1.6.0.md"
  expect fail "$dir/bad/v1.6.0.md" ":4: bullet must end with its PR number"
  expect fail "$dir/bad/v1.6.0.md" ":5: section text must be '- ' bullets"
  expect fail "$dir/bad/v1.6.0.md" ":7: section has no bullets"
  expect fail "$dir/bad/v9.9.9.md" "missing or empty"
  [ "$fail" = 0 ] && echo "check-release-notes self-test: ok"
  return "$fail"
}

if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit $?
fi

if [ "$#" = 0 ]; then
  set -- "$(dirname "$0")/../docs/releases"/v*.md
fi
status=0
for f in "$@"; do
  check_file "$f" || status=1
done
[ "$status" = 0 ] && echo "release notes: $# file(s) ok"
exit "$status"
