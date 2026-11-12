#!/bin/sh
#
# quiet.sh — run a command with its output in a log file.
#
# Usage: tools/quiet.sh <name> <command> [args...]
#
# The full output goes to tmp/logs/<name>.log. On success it prints one
# line. On failure it prints the error lines it finds in the log (Go
# test, compile, vet and staticcheck errors, panics, Gradle and Kotlin
# errors) and the log path, then exits with the command's status. When
# no known error shape matches, it prints the last lines of the log.

set -u

name=$1
shift

root=$(cd "$(dirname "$0")/.." && pwd)
logdir=$root/tmp/logs
log=$logdir/$name.log
mkdir -p "$logdir"

start=$(date +%s)
"$@" >"$log" 2>&1
status=$?
secs=$(( $(date +%s) - start ))

if [ "$status" -eq 0 ]; then
    echo "$name: ok (${secs}s)"
    exit 0
fi

echo "$name: FAILED (exit $status, ${secs}s)"

# A matched line prints, and some shapes also print the lines after
# them: a panic's first stack frames, a failed Gradle test's assertion,
# Gradle's "What went wrong" block. The extract stops at max lines.
awk -v max=60 '
    function out(s) { if (n < max) print s; n++ }
    after > 0 { out($0); after--; next }
    /^panic:/                         { out($0); after = 12; next }
    /What went wrong:/                { out($0); after = 4;  next }
    / FAILED$/                        { out($0); after = 3;  next }
    /^[ \t]*--- FAIL|^FAIL[ \t]/      { out($0); next }
    /(^|[ \t])[^ \t:]+\.go:[0-9]+:/   { out($0); next }
    /^(e|error|ERROR|FATAL): |: error: / { out($0); next }
    /^make(\[[0-9]+\])?: \*\*\*/      { out($0); next }
    END {
        if (n > max) print "... " n - max " more matched lines in the log"
        exit n == 0
    }
' "$log" || tail -n 30 "$log"

echo "full log: $log"
exit "$status"
