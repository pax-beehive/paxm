#!/bin/sh

set -eu

files=$(find cmd internal \
	-name '*.go' \
	-not -name '*_test.go' \
	-not -path 'internal/eval/*' \
	-not -path 'internal/crossagent/*' \
	-not -path 'internal/adapters/contracttest/*' \
	-not -path 'internal/adapters/jsonrpc/conformance/*' \
	-print)

if [ -z "$files" ]; then
	echo "no production Go files found" >&2
	exit 1
fi

cyclo=$(gocyclo $files | awk '$3 !~ /Eval|LoCoMo|Conformance/ {print}')
cognitive=$(gocognit $files | awk '$3 !~ /Eval|LoCoMo|Conformance/ {print}')

if printf '%s\n' "$cyclo" | awk '$1 > 20 {print; found=1} END {exit found ? 1 : 0}'; then
	:
else
	echo "production cyclomatic complexity exceeds 20" >&2
	exit 1
fi

if printf '%s\n' "$cognitive" | awk '$1 > 25 {print; found=1} END {exit found ? 1 : 0}'; then
	:
else
	echo "production cognitive complexity exceeds 25" >&2
	exit 1
fi

echo "production complexity gate passed: cyclomatic <= 20, cognitive <= 25"
