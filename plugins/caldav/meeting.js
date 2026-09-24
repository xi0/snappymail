// Mailbux CalDAV - iCalendar location helper
// Decodes iCalendar TEXT values and pulls video-conferencing links (Google
// Meet, Zoom, Microsoft Teams, Webex, …) out of a location string. Shared by
// calendar-dialog.js (event form) and message.js (received invite box) and
// exposed on window.MailbuxCalDavMeeting.
(() => {
'use strict';

// RFC 5545 3.3.11 TEXT unescaping in a single pass, so an escaped backslash is
// handled correctly (e.g. "\\n" is a literal backslash followed by "n", not a
// newline): \\ -> \, \, -> ',', \; -> ';', \n / \N -> newline.
function unescapeICalText(value) {
	return ('' + (value == null ? '' : value)).replace(/\\([\\;,nN])/g, (match, ch) => {
		switch (ch) {
			case 'n':
			case 'N':
				return '\n';
			case '\\':
				return '\\';
			case ',':
				return ',';
			case ';':
				return ';';
			default:
				return match;
		}
	});
}

// Hosts that are known to serve online meetings / video calls. A subdomain of
// any of these matches too (e.g. us02web.zoom.us).
const KNOWN_MEETING_HOSTS = [
	'meet.google.com',
	'zoom.us',
	'teams.microsoft.com',
	'teams.live.com',
	'webex.com',
	'whereby.com',
	'jit.si',
	'gotomeeting.com',
	'gotomeet.me',
	'bluejeans.com',
	'skype.com',
	'skype.me',
	'discord.com',
	'discord.gg',
	'8x8.vc',
	'chime.aws',
	'amazonchime.com',
	'voovmeeting.com',
	'around.co'
];

// Host labels that strongly suggest a meeting link when the exact host is not
// in the known list (e.g. a self-hosted "meet.example.com").
const MEETING_LABELS = ['meet', 'meeting', 'zoom', 'teams', 'webex', 'jitsi',
	'whereby', 'gotomeet', 'bluejeans', 'chime', 'videoconf'];

function hostOf(url) {
	try {
		return new URL(url).hostname.toLowerCase();
	} catch (e) {
		return '';
	}
}

function isMeetingUrl(url) {
	const host = hostOf(url);
	if (!host) {
		return false;
	}
	if (KNOWN_MEETING_HOSTS.some(h => host === h || host.endsWith('.' + h))) {
		return true;
	}
	return host.split('.').some(label => MEETING_LABELS.includes(label));
}

// Find the first http(s) URL in an (already unescaped) string that looks like a
// meeting link, or '' when there is none.
function findMeetingUrl(text) {
	const urls = ('' + (text == null ? '' : text)).match(/https?:\/\/[^\s<>"'\])]+/gi) || [];
	for (let i = 0; i < urls.length; i++) {
		const url = urls[i].replace(/[.,;:!?]+$/, '');
		if (isMeetingUrl(url)) {
			return url;
		}
	}
	return '';
}

// Detect a meeting link inside a raw iCalendar LOCATION value.
function meetingLinkFor(value) {
	return findMeetingUrl(unescapeICalText(value));
}

// Split a raw iCalendar LOCATION value into the plain address and the meeting
// link it may carry (Google stores "Address; https://meet.google.com/…").
function splitLocation(value) {
	const text = unescapeICalText(value);
	const meetingUrl = findMeetingUrl(text);
	if (!meetingUrl) {
		return {location: text.trim(), meetingUrl: ''};
	}
	const location = text.split(meetingUrl).join(' ')
		.replace(/\s*;\s*/g, ', ')
		.replace(/\s{2,}/g, ' ')
		.replace(/^[\s,;]+/, '')
		.replace(/[\s,;]+$/, '')
		.trim();
	return {location: location, meetingUrl: meetingUrl};
}

window.MailbuxCalDavMeeting = {
	unescapeICalText: unescapeICalText,
	meetingLinkFor: meetingLinkFor,
	splitLocation: splitLocation
};
})();
