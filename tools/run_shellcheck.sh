#!/bin/sh
#
# Runs shellcheck on all shell scripts in the istio repository.

TOOLS_DIR="$(cd "$(dirname "${0}")" && pwd -P)"
ISTIO_ROOT="$(cd "$(dirname "${TOOLS_DIR}")" && pwd -P)"

# All files ending in .sh.
SH_FILES=$( \
    find "${ISTIO_ROOT}" \
        -name '*.sh' -type f \
        -not -path '*/vendor/*' \
        -not -path '*/.git/*')
# All files not ending in .sh but starting with a shebang.
SHEBANG_FILES=$( \
    find "${ISTIO_ROOT}" \
        -not -name '*.sh' -type f \
        -not -path '*/vendor/*' \
        -not -path '*/.git/*' | \
        while read -r f; do
            head -n 1 "$f" | grep -q '^#!.*sh' && echo "$f";
        done)

# Set global rule exclusions with the "excludes" flag, separated by comma (e.g.
# "--excludes=SC1090,SC1091"). See https://github.com/koalaman/shellcheck/wiki
# for details on each code's corresponding rule.

echo "${SH_FILES}" "${SHEBANG_FILES}" \
    | xargs shellcheck
