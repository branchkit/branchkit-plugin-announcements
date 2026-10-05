#!/usr/bin/env bash
# Check a packaged release archive: both programs present and executable, the
# engine's licence notices beside it, and no source or build directories.
#
#   scripts/check-archive.sh dist/branchkit-plugin-announcements-<os>-<arch>.tar.gz
set -euo pipefail
archive=$1
listing=$(tar -tvzf "$archive")
echo "$listing"
fail=0
for f in announcements-plugin sherpa_tts; do
  if ! echo "$listing" | grep -Eq "^-rwxr-xr-x .* $f(\.exe)?$"; then
    echo "MISSING or not executable: $f" >&2; fail=1
  fi
done
for f in plugin.json LICENSE NOTICE LICENSE-GPL-3.0; do
  if ! echo "$listing" | grep -Eq " $f$"; then
    echo "MISSING: $f" >&2; fail=1
  fi
done
if echo "$listing" | grep -Eq ' (stages|src|scripts)/'; then
  echo "source or build files leaked into the archive" >&2; fail=1
fi
exit $fail
