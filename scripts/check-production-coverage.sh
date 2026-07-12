#!/bin/sh

set -eu

profile=${1:-coverage.out}
required=${2:-75}

if [ ! -f "$profile" ]; then
	echo "coverage profile not found: $profile" >&2
	exit 1
fi

awk -v required="$required" '
NR == 1 { next }
{
	split($1, parts, ":")
	path = parts[1]
	if (path ~ /\/evals\// ||
	    path ~ /\/examples\// ||
	    path ~ /\/internal\/eval\// ||
	    path ~ /\/internal\/crossagent\// ||
	    path ~ /\/internal\/adapters\/contracttest\// ||
	    path ~ /\/internal\/adapters\/jsonrpc\/conformance\//) {
		next
	}
	total += $2
	if ($3 > 0) {
		covered += $2
	}
}
END {
	if (total == 0) {
		print "no production statements found" > "/dev/stderr"
		exit 1
	}
	percent = covered * 100 / total
	printf "non-eval product coverage: %.1f%% (%d/%d), required: %.1f%%\n", percent, covered, total, required
	if (percent + 0.000001 < required) {
		exit 1
	}
}' "$profile"
