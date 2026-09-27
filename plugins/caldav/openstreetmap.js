// Mailbux CalDAV - OpenStreetMap helper
// Shared by calendar-dialog.js (event window) and message.js (received invite
// box). Detects whether a location string looks like a postal address and, if
// so, builds an OpenStreetMap search link plus the required OpenStreetMap
// attribution. Exposed on window.MailbuxCalDavOsm so both IIFEs can reuse it.
(() => {
'use strict';

// Standard OpenStreetMap attribution. Kept in English as it is the canonical
// attribution string required by the OpenStreetMap Foundation.
const ATTRIBUTION = '© OpenStreetMap contributors';
const COPYRIGHT_URL = 'https://www.openstreetmap.org/copyright';

// Does the location look like a (postal) address rather than a room name,
// "Online", a meeting URL, etc.? Deliberately conservative but tolerant of
// international formats:
//   - must be a reasonable length and contain letters
//   - must not be a URL or an e-mail address
//   - must contain a house number / postal code (a digit) or comma separated
//     parts (e.g. "Baker Street, London")
function looksLikeAddress(value) {
	const s = ('' + (value == null ? '' : value)).trim();
	if (s.length < 4) {
		return false;
	}
	// URLs and e-mail addresses are not addresses
	if (/^[a-z][a-z0-9+.-]*:\/\//i.test(s) || /@/.test(s)) {
		return false;
	}
	// Need at least one letter (skip pure numbers/coordinates)
	if (!/[A-Za-z\u00C0-\u024F]/.test(s)) {
		return false;
	}
	return /\d/.test(s) || s.indexOf(',') !== -1;
}

// OpenStreetMap search URL for an address / place.
function searchUrl(value) {
	return 'https://www.openstreetmap.org/search?query='
		+ encodeURIComponent(('' + (value == null ? '' : value)).trim());
}

window.MailbuxCalDavOsm = {
	attribution: ATTRIBUTION,
	copyrightUrl: COPYRIGHT_URL,
	looksLikeAddress: looksLikeAddress,
	searchUrl: searchUrl
};
})();
