#!/usr/bin/env bash
set -e

COVERAGE_MIN_GLOBAL=${COVERAGE_MIN_GLOBAL:-85}
COVERAGE_MIN_CORE=${COVERAGE_MIN_CORE:-90}
CORE_PKGS_PATTERN=${CORE_PKGS_PATTERN:-"internal/(completion|fallback|protocol)"}

COVERAGE_FILE=${1:-coverage.out}
CORE_COVERAGE_FILE="core_coverage.out"

if [ ! -f "$COVERAGE_FILE" ]; then
    echo "❌ Error: coverage file '$COVERAGE_FILE' not found."
    exit 1
fi

# Make sure we clean up the temporary file when the script exits (success or fail)
trap 'rm -f "$CORE_COVERAGE_FILE"' EXIT

# 1. Calculate global coverage
total=$(go tool cover -func="$COVERAGE_FILE" | awk '/^total:/ {gsub("%", "", $3); print $3}')

# 2. Extract core packages coverage
echo "mode: atomic" > "$CORE_COVERAGE_FILE"
grep -E "$CORE_PKGS_PATTERN" "$COVERAGE_FILE" >> "$CORE_COVERAGE_FILE" || true

core_total=$(go tool cover -func="$CORE_COVERAGE_FILE" | awk '/^total:/ {gsub("%", "", $3); print $3}')
if [ -z "$core_total" ]; then
    core_total="0.0"
fi

# 3. Validate thresholds using awk to handle floating point comparisons
awk -v total="$total" -v min_global="$COVERAGE_MIN_GLOBAL" \
    -v core="$core_total" -v min_core="$COVERAGE_MIN_CORE" 'BEGIN {
    failed=0;
    printf "global coverage: %.1f%% (minimum %.1f%%)\n", total, min_global;
    printf "core coverage: %.1f%% (minimum %.1f%%)\n", core, min_core;
    if (total + 0 < min_global + 0) {
        printf "❌ global coverage failed\n";
        failed=1;
    }
    if (core + 0 < min_core + 0) {
        printf "❌ core coverage failed\n";
        failed=1;
    }
    if (failed) exit 1;
}'
