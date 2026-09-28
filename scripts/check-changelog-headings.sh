#!/bin/sh
# Fails when CHANGELOG.md repeats a `## [release]` heading, or a `### ` heading
# within one release section.
#
# Usage: check-changelog-headings.sh [FILE]
#   FILE defaults to CHANGELOG.md in the current directory.
#   Exit 0 = clean, 1 = duplicates found, 2 = file missing.

set -e

file="${1:-CHANGELOG.md}"

if [ ! -f "$file" ]; then
  echo "error: $file not found" >&2
  exit 2
fi

awk '
  { sub(/[ \t\r]+$/, "") }
  /^## \[/ {
    section = $0
    if (section in sections) {
      printf "duplicate: %s (lines %d and %d)\n", section, sections[section], NR > "/dev/stderr"
      rc = 1
    } else {
      sections[section] = NR
    }
    next
  }
  /^### / {
    key = section SUBSEP $0
    if (key in seen) {
      printf "duplicate: %s under %s (lines %d and %d)\n", $0, section, seen[key], NR > "/dev/stderr"
      rc = 1
    } else {
      seen[key] = NR
    }
  }
  END { exit (rc ? 1 : 0) }
' "$file"
