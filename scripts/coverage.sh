#!/usr/bin/env bash
#
# Fail if total statement coverage falls below the threshold (default 90%).
#
# One script so `make cover-check` and CI enforce the same number - a gate that
# only runs in CI is one everyone discovers late. Override with:
#   COVERAGE_THRESHOLD=95 scripts/coverage.sh
set -euo pipefail

cd "$(dirname "$0")/.."

threshold="${COVERAGE_THRESHOLD:-90}"
profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT

# One run produces both the per-package lines (stdout) and the merged profile.
output="$(go test ./... -coverprofile="$profile" -covermode=atomic 2>&1)" || {
	echo "$output"
	echo "FAIL: tests did not pass" >&2
	exit 1
}

echo "$output" | grep -E "coverage:|no test files" | sed 's|github.com/openzot/openzot||'

total="$(go tool cover -func="$profile" | awk '/^total:/ {print $3}' | tr -d '%')"

echo "----"
echo "total coverage: ${total}%  (threshold ${threshold}%)"

if awk -v t="$total" -v th="$threshold" 'BEGIN { exit (t+0 < th+0) ? 0 : 1 }'; then
	echo "FAIL: coverage ${total}% is below the ${threshold}% threshold" >&2
	echo "add tests for the packages below the line above, or lower COVERAGE_THRESHOLD deliberately" >&2
	exit 1
fi

echo "OK: coverage is at or above ${threshold}%"
