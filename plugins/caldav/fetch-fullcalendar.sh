#!/usr/bin/env bash
#
# Downloads the FullCalendar 6 "global" bundle (dayGrid + timeGrid + list
# plugins and the stylesheet, all in one file) into this plugin's
# `fullcalendar/` folder.
#
# This is required because SnappyMail's Content-Security-Policy only allows
# scripts from 'self' (plus a per-request nonce) and blocks cdn.jsdelivr.net.
# Once the file is present, plugins/caldav/index.php bundles it via addJs(),
# so it is served from the SnappyMail origin and the CSP is satisfied.
#
# Usage:
#   ./fetch-fullcalendar.sh              # uses the pinned version below
#   ./fetch-fullcalendar.sh 6.1.15       # explicit version
#
set -euo pipefail

VERSION="${1:-6.1.15}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST_DIR="${SCRIPT_DIR}/fullcalendar"
DEST_FILE="${DEST_DIR}/index.global.min.js"
URL="https://cdn.jsdelivr.net/npm/fullcalendar@${VERSION}/index.global.min.js"

mkdir -p "${DEST_DIR}"

echo "Downloading FullCalendar ${VERSION}"
echo "  ${URL}"
echo "  -> ${DEST_FILE}"

if command -v curl >/dev/null 2>&1; then
	curl -fSL "${URL}" -o "${DEST_FILE}"
elif command -v wget >/dev/null 2>&1; then
	wget -O "${DEST_FILE}" "${URL}"
else
	echo "ERROR: neither curl nor wget is available." >&2
	exit 1
fi

if ! grep -q "FullCalendar" "${DEST_FILE}"; then
	echo "ERROR: downloaded file does not look like a FullCalendar bundle." >&2
	exit 1
fi

echo
echo "Done. FullCalendar ${VERSION} installed at:"
echo "  ${DEST_FILE}"
echo "Reload SnappyMail (admin panel + webmail) to pick up the change."
