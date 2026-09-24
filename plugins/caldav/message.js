// Mailbux CalDAV Auto - Calendar invite (received as .ics attachment)
// When a message contains a text/calendar attachment, show an invite box in the
// message view that lets the user add the event to their calendar and/or respond
// to the invitation (Accept / Tentative / Decline). Recurring invites show the
// recurrence pattern, per-occurrence updates (RECURRENCE-ID) are applied to that
// occurrence only, and METHOD:CANCEL offers "Remove from calendar".
// All calendar work is done by the caldav plugin backend
// (ImportCalendarEvent / RespondToEvent / RemoveCalendarEvent JSON actions).
((rl) => {
'use strict';

const templateId = 'MailMessageView';

// Resolve a label through the plugin's own CALDAV i18n namespace.
function t(key, fallback, params) {
	let value = '';
	try {
		value = (window.rl && rl.i18n) ? rl.i18n('CALDAV/' + key) : '';
	} catch (e) {
		value = '';
	}
	if (!value || value === 'CALDAV/' + key) {
		value = fallback || '';
	}
	if (params) {
		Object.keys(params).forEach(name => {
			value = value.replace('%' + name + '%', params[name]);
		});
	}
	return value;
}

function esc(value) {
	return (value == null ? '' : '' + value)
		.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
		.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

// OpenStreetMap helper exposed by openstreetmap.js (loaded before this file).
// Falls back to safe defaults so the invite box never throws if it is missing.
const OSM = window.MailbuxCalDavOsm || {};
const OSM_ATTRIBUTION = OSM.attribution || '© OpenStreetMap contributors';
const OSM_COPYRIGHT_URL = OSM.copyrightUrl || 'https://www.openstreetmap.org/copyright';

// iCalendar TEXT decoding + meeting-link detection exposed by meeting.js
// (loaded before this file). Falls back to safe defaults when it is missing.
const Meeting = window.MailbuxCalDavMeeting || {};

// OpenStreetMap search URL for an address-like location ('' otherwise).
function osmLinkFor(location) {
	return (OSM.looksLikeAddress && OSM.looksLikeAddress(location))
		? OSM.searchUrl(location)
		: '';
}

/* ------------------------------------------------ iCalendar parsing */

// Unfold folded lines (RFC 5545 3.1) into logical lines.
function unfoldIcs(text) {
	const out = [];
	('' + text).replace(/\r\n|\r/g, '\n').split('\n').forEach(line => {
		if (line && (line[0] === ' ' || line[0] === '\t')) {
			if (out.length) {
				out[out.length - 1] += line.slice(1);
			}
		} else {
			out.push(line);
		}
	});
	return out;
}

function unescapeText(value) {
	return Meeting.unescapeICalText
		? Meeting.unescapeICalText(value)
		: ('' + (value == null ? '' : value));
}

// Split a raw iCalendar LOCATION value into the plain address and the meeting
// link it may carry (e.g. "Bautavej 9, 8210 Aarhus; https://meet.google.com/…").
function splitLocation(value) {
	return Meeting.splitLocation
		? Meeting.splitLocation(value)
		: {location: unescapeText(value), meetingUrl: ''};
}

// Parse the first VEVENT of an iCalendar string.
function parseInvite(text) {
	const invite = {method: '', hasEvent: false, props: {}};
	let inEvent = false;
	unfoldIcs(text).forEach(line => {
		if (line === 'BEGIN:VEVENT') {
			inEvent = true;
			invite.hasEvent = true;
			return;
		}
		if (line === 'END:VEVENT') {
			inEvent = false;
			return;
		}
		const pos = line.indexOf(':');
		if (pos === -1) {
			return;
		}
		const head = line.slice(0, pos);
		const value = line.slice(pos + 1);
		const parts = head.split(';');
		const name = parts.shift().toUpperCase();
		if (!inEvent) {
			if (name === 'METHOD') {
				invite.method = value.trim().toUpperCase();
			}
			return;
		}
		const params = {};
		parts.forEach(part => {
			const kv = part.split('=');
			if (kv.length === 2) {
				params[kv[0].trim().toUpperCase()] = kv[1].trim().replace(/^"|"$/g, '');
			}
		});
		if (!invite.props[name]) {
			invite.props[name] = [];
		}
		invite.props[name].push({params: params, value: value});
	});
	return invite;
}

function prop(invite, name, fallback) {
	const list = invite.props[name.toUpperCase()];
	return (list && list.length) ? list[0].value : (fallback || '');
}

function mailAddress(value) {
	return ('' + (value || '')).replace(/^mailto:/i, '').replace(/^<|>$/g, '').trim();
}

// Offset (ms) of an IANA timezone at a given instant: zoneWall - utc.
function tzOffsetMs(ts, tzid) {
	const dtf = new Intl.DateTimeFormat('en-US', {
		timeZone: tzid, hourCycle: 'h23',
		year: 'numeric', month: '2-digit', day: '2-digit',
		hour: '2-digit', minute: '2-digit', second: '2-digit'
	});
	const map = {};
	dtf.formatToParts(new Date(ts)).forEach(part => { map[part.type] = part.value; });
	if (map.hour === '24') {
		map.hour = '00';
	}
	const asUtc = Date.UTC(+map.year, +map.month - 1, +map.day, +map.hour, +map.minute, +map.second);
	return asUtc - ts;
}

// Build the UTC instant for a wall-clock time in the given IANA timezone.
function zonedWallToUtc(y, month, day, hour, minute, second, tzid) {
	const wall = Date.UTC(y, month - 1, day, hour, minute, second);
	if (!tzid) {
		return new Date(wall);
	}
	try {
		const off1 = tzOffsetMs(wall, tzid);
		let ts = wall - off1;
		const off2 = tzOffsetMs(ts, tzid);
		if (off2 !== off1) {
			ts = wall - off2;
		}
		return new Date(ts);
	} catch (e) {
		// Unknown timezone in this browser: fall back to treating it as UTC
		return new Date(wall);
	}
}

// Read the TZID parameter of a VEVENT property (e.g. DTSTART;TZID=...).
function propTzid(invite, name) {
	const list = invite.props[name.toUpperCase()];
	return (list && list.length && list[0].params && list[0].params.TZID) || '';
}

function pad2(n) {
	return (n < 10 ? '0' : '') + n;
}

// Rewrite a single iCalendar line carrying a TZID into explicit UTC.
function convertTzidLineToUtc(line) {
	const pos = line.indexOf(':');
	if (pos === -1) {
		return line;
	}
	const head = line.slice(0, pos);
	if (!/;TZID=/i.test(head)) {
		return line;
	}
	const name = (head.split(';')[0] || '').toUpperCase();
	if (['DTSTART', 'DTEND', 'RECURRENCE-ID', 'RDATE', 'EXDATE'].indexOf(name) === -1) {
		return line;
	}
	const tzMatch = head.match(/;TZID=([^;:]+)/i);
	if (!tzMatch) {
		return line;
	}
	const tzid = tzMatch[1].replace(/^"|"$/g, '');
	const newHead = head.replace(/;TZID=[^;:]+/i, '');
	const values = line.slice(pos + 1).split(',').map(v => v.trim()).filter(Boolean);
	const converted = [];
	for (let i = 0; i < values.length; i++) {
		const v = values[i];
		const m = v.match(/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})(Z)?$/);
		if (!m) {
			// Not a plain local datetime (e.g. a DATE): leave the line untouched
			return line;
		}
		if (m[7]) {
			converted.push(v);
			continue;
		}
		const d = zonedWallToUtc(+m[1], +m[2], +m[3], +m[4], +m[5], +m[6], tzid);
		converted.push(d.getUTCFullYear() + pad2(d.getUTCMonth() + 1) + pad2(d.getUTCDate())
			+ 'T' + pad2(d.getUTCHours()) + pad2(d.getUTCMinutes()) + pad2(d.getUTCSeconds()) + 'Z');
	}
	return converted.length ? (newHead + ':' + converted.join(',')) : line;
}

// Rewrite DTSTART/DTEND/RECURRENCE-ID/RDATE/EXDATE lines that carry a TZID into
// explicit UTC and drop the now-unused VTIMEZONE blocks, so the stored CalDAV
// event keeps its time and does not depend on a VTIMEZONE component (or the
// server's timezone database).
function convertIcsTzidToUtc(text) {
	const out = [];
	let inVtimezone = false;
	unfoldIcs(text).forEach(line => {
		if (/^BEGIN:VTIMEZONE\b/i.test(line)) {
			inVtimezone = true;
			return;
		}
		if (/^END:VTIMEZONE\b/i.test(line)) {
			inVtimezone = false;
			return;
		}
		if (inVtimezone) {
			return;
		}
		out.push(convertTzidLineToUtc(line));
	});
	return out.join('\r\n');
}

// Format an iCalendar date (20251106T143000Z / 20251106T143000 / 20251106) for
// display. A floating time carrying a TZID is resolved in that timezone.
function formatIcsDate(value, tzid) {
	const str = '' + (value || '');
	const m = str.match(/^(\d{4})(\d{2})(\d{2})(?:T(\d{2})(\d{2})(\d{2})(Z)?)?$/);
	if (!m) {
		return str;
	}
	try {
		let date;
		let options;
		if (m[4]) {
			date = m[7]
				? new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]))
				: zonedWallToUtc(+m[1], +m[2], +m[3], +m[4], +m[5], +m[6], tzid);
			options = {dateStyle: 'medium', timeStyle: 'short'};
		} else {
			date = new Date(+m[1], +m[2] - 1, +m[3]);
			options = {dateStyle: 'full'};
		}
		return (typeof date.format === 'function')
			? date.format(options)
			: date.toLocaleString(undefined, options);
	} catch (e) {
		return str;
	}
}

// Localized short weekday name for a 0=Sunday index.
function weekdayName(index) {
	try {
		return new Intl.DateTimeFormat(document.documentElement.lang || undefined, {weekday: 'short'})
			.format(new Date(2024, 0, 7 + index));
	} catch (e) {
		return ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][index] || '';
	}
}

// Human readable summary of an invite's RRULE ('' when not recurring).
function recurrenceText(invite) {
	const rrule = prop(invite, 'RRULE', '');
	if (!rrule) {
		return '';
	}
	const rule = {};
	rrule.split(';').forEach(part => {
		const i = part.indexOf('=');
		if (i > 0) {
			rule[part.slice(0, i).toUpperCase()] = part.slice(i + 1);
		}
	});
	const freq = (rule.FREQ || '').toUpperCase();
	const freqText = t('FREQ_' + freq, freq.toLowerCase());
	if (!freqText) {
		return '';
	}
	const interval = parseInt(rule.INTERVAL || '1', 10) || 1;
	let text = freqText;
	if (interval > 1) {
		text = interval + ' × ' + text;
	}
	if (rule.BYDAY) {
		const days = rule.BYDAY.split(',').map(code => {
			const idx = ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'].indexOf(('' + code).replace(/[^A-Za-z]/g, '').toUpperCase());
			return idx >= 0 ? weekdayName(idx) : code;
		});
		text += ' ' + t('REPEAT_ON', 'on') + ' ' + days.join(', ');
	}
	if (rule.COUNT) {
		text += ', ' + rule.COUNT + '×';
	} else if (rule.UNTIL) {
		text += ', ' + t('REPEAT_UNTIL', 'until') + ' ' + formatIcsDate(rule.UNTIL);
	}
	return text;
}

/* ------------------------------------------------ request helper */

function request(action, params) {
	return new Promise((resolve, reject) => {
		if (!window.rl || typeof rl.pluginRemoteRequest !== 'function') {
			reject(new Error(t('REMOTE_NOT_AVAILABLE', 'Remote not available')));
			return;
		}
		rl.pluginRemoteRequest((iError, oData) => {
			if (iError || !oData || !oData.Result) {
				reject(new Error((oData && (oData.message || oData.error)) || t('REQUEST_FAILED', 'Request failed')));
			} else {
				resolve(oData.Result);
			}
		}, action, params || {});
	});
}

/* ------------------------------------------------ message view hook */

addEventListener('rl-view-model.create', e => {
	if (templateId !== e.detail.viewModelTemplateID) {
		return;
	}

	const template = document.getElementById(templateId);
	const view = e.detail;
	const attachmentsPlace = template.content.querySelector('.attachmentsPlace');
	if (!attachmentsPlace) {
		return;
	}

	// Inject the invite box into the template only once
	if (!template.content.querySelector('.caldavInvite')) {
		attachmentsPlace.after(Element.fromHTML(`
			<div class="caldavInvite" data-bind="if: CalDavInvite">
				<details open>
					<summary>
						<span data-icon="📅"></span>
						<span data-bind="text: CalDavInvite() && CalDavInvite().summary"></span>
					</summary>
					<table class="caldavInviteInfo"><tbody>
						<tr data-bind="visible: CalDavInvite() && CalDavInvite().organizer">
							<td>${esc(t('ORGANIZER', 'Organizer'))}:</td>
							<td data-bind="text: CalDavInvite() && CalDavInvite().organizer"></td>
						</tr>
						<tr data-bind="visible: CalDavInvite() && CalDavInvite().start">
							<td>${esc(t('START', 'Start'))}:</td>
							<td data-bind="text: CalDavInvite() && CalDavInvite().start"></td>
						</tr>
						<tr data-bind="visible: CalDavInvite() && CalDavInvite().end">
							<td>${esc(t('END', 'End'))}:</td>
							<td data-bind="text: CalDavInvite() && CalDavInvite().end"></td>
						</tr>
						<tr data-bind="visible: CalDavInvite() && CalDavInvite().location">
							<td>${esc(t('LOCATION', 'Location'))}:</td>
							<td>
								<span class="caldavInviteLocation" data-bind="text: CalDavInvite() && CalDavInvite().location"></span>
								<span class="caldavInviteMap" data-bind="visible: CalDavLocationUrl">
									<a class="caldavInviteMapLink" target="_blank" rel="noopener noreferrer"
										data-bind="attr: {href: CalDavLocationUrl}, text: CalDavOpenMapLabel"></a>
									<a class="caldavInviteMapAttr" target="_blank" rel="noopener noreferrer"
										data-bind="attr: {href: CalDavOsmCopyrightUrl}, text: CalDavOsmAttribution"></a>
								</span>
							</td>
						</tr>
						<tr data-bind="visible: CalDavInvite() && CalDavInvite().meetingUrl">
							<td>${esc(t('MEETING', 'Meeting'))}:</td>
							<td>
								<a class="caldavInviteMeeting" target="_blank" rel="noopener noreferrer"
									data-bind="attr: {href: CalDavInvite() && CalDavInvite().meetingUrl}, text: CalDavJoinMeetingLabel"></a>
							</td>
						</tr>
						<tr data-bind="visible: CalDavInvite() && CalDavInvite().recurrence">
							<td>${esc(t('REPEAT', 'Repeat'))}:</td>
							<td data-bind="text: CalDavInvite() && CalDavInvite().recurrence"></td>
						</tr>
					</tbody></table>
					<div class="caldavInviteActions">
						<select class="caldavInviteCalendar"
							data-bind="visible: (CalDavCanAdd() || CalDavCanRemove()) && CalDavCalendars().length > 1, options: CalDavCalendars, optionsText: 'name', optionsValue: 'id', value: CalDavCalendarId"></select>
						<button type="button" class="btn btn-success caldavInviteAdd"
							data-bind="visible: CalDavCanAdd, click: caldavAddEvent, disable: CalDavBusy(), text: caldavAddText"></button>
						<button type="button" class="btn btn-danger caldavInviteRemove"
							data-bind="visible: CalDavCanRemove, click: caldavRemoveEvent, disable: CalDavBusy()">${esc(t('REMOVE_FROM_CALENDAR', 'Remove from calendar'))}</button>
						<span class="caldavInviteRespond" data-bind="visible: CalDavCanRespond">
							<button type="button" class="btn btn-success caldavInviteAccept"
								data-bind="click: () => caldavRespond('ACCEPTED'), disable: CalDavBusy()">${esc(t('ACCEPT', 'Accept'))}</button>
							<button type="button" class="btn caldavInviteTentative"
								data-bind="click: () => caldavRespond('TENTATIVE'), disable: CalDavBusy()">${esc(t('TENTATIVE', 'Tentative'))}</button>
							<button type="button" class="btn btn-danger caldavInviteDecline"
								data-bind="click: () => caldavRespond('DECLINED'), disable: CalDavBusy()">${esc(t('DECLINE', 'Decline'))}</button>
						</span>
					</div>
					<div class="caldavInviteStatus" data-bind="visible: CalDavStatus, text: CalDavStatus"></div>
					<div class="caldavInviteError" data-bind="visible: CalDavError, text: CalDavError"></div>
				</details>
			</div>`));
	}

	view.CalDavInvite = ko.observable(null);
	view.CalDavCalendars = ko.observableArray([]);
	view.CalDavCalendarId = ko.observable('');
	view.CalDavCanAdd = ko.observable(true);
	view.CalDavCanRespond = ko.observable(false);
	view.CalDavCanRemove = ko.observable(false);
	view.CalDavBusy = ko.observable(false);
	view.CalDavStatus = ko.observable('');
	view.CalDavError = ko.observable('');
	view.CalDavAdded = ko.observable(false);
	view.CalDavLocationUrl = ko.observable('');
	view.CalDavOpenMapLabel = t('OPEN_IN_OPENSTREETMAP', 'Open in OpenStreetMap');
	view.CalDavJoinMeetingLabel = t('JOIN_MEETING', 'Join meeting');
	view.CalDavOsmCopyrightUrl = OSM_COPYRIGHT_URL;
	view.CalDavOsmAttribution = OSM_ATTRIBUTION;
	view.caldavAddText = ko.computed(() =>
		view.CalDavAdded() ? t('ADDED', 'Added to calendar') : t('ADD_TO_CALENDAR', 'Add to calendar'));

	const showError = message => view.CalDavError(message || t('REQUEST_FAILED', 'Request failed'));

	const loadCalendars = () => {
		request('GetCalendars', {}).then(result => {
			const calendars = (result && result.calendars) || [];
			if (calendars.length) {
				view.CalDavCalendars(calendars);
				view.CalDavCalendarId(calendars[0].id);
			} else {
				view.CalDavCalendars([{id: 'default', name: t('CALENDAR', 'Calendar')}]);
				view.CalDavCalendarId('default');
			}
		}).catch(() => {
			view.CalDavCalendars([{id: 'default', name: t('CALENDAR', 'Calendar')}]);
			view.CalDavCalendarId('default');
		});
	};

	view.caldavAddEvent = () => {
		const invite = view.CalDavInvite();
		if (!invite || view.CalDavBusy()) {
			return;
		}
		const doAdd = () => {
			view.CalDavError('');
			view.CalDavBusy(true);
			request('ImportCalendarEvent', {
				Ics: convertIcsTzidToUtc(invite.rawText),
				CalendarId: view.CalDavCalendarId() || 'default'
			}).then(result => {
				view.CalDavBusy(false);
				if (result && result.success) {
					view.CalDavAdded(true);
					invite.uid = result.uid || invite.uid;
					view.CalDavStatus(t('ADDED', 'Added to calendar'));
				} else {
					showError(result && result.error);
				}
			}).catch(err => {
				view.CalDavBusy(false);
				showError(err && err.message);
			});
		};

		const message = t('ADD_CONFIRM', 'Do you want to add this event to your calendar?');
		if (window.rl && rl.app && rl.app.ask && typeof rl.app.ask.showModal === 'function') {
			rl.app.ask.showModal([message, doAdd, null]);
		} else if (window.confirm(message)) {
			doAdd();
		}
	};

	view.caldavRespond = response => {
		const invite = view.CalDavInvite();
		if (!invite || view.CalDavBusy()) {
			return;
		}
		view.CalDavError('');
		view.CalDavBusy(true);
		request('RespondToEvent', {
			Ics: invite.rawText,
			Response: response,
			CalendarId: view.CalDavAdded() ? (view.CalDavCalendarId() || 'default') : '',
			Uid: invite.uid || '',
			RecurrenceId: invite.recurrenceId || ''
		}).then(result => {
			view.CalDavBusy(false);
			if (result && result.success) {
				view.CalDavStatus(t('RESPONDED', 'Response sent to organizer'));
			} else {
				showError(result && result.error);
			}
		}).catch(err => {
			view.CalDavBusy(false);
			showError(err && err.message);
		});
	};

	view.caldavRemoveEvent = () => {
		const invite = view.CalDavInvite();
		if (!invite || view.CalDavBusy()) {
			return;
		}
		const recurrenceId = invite.recurrenceId || '';
		const doRemove = () => {
			view.CalDavError('');
			view.CalDavBusy(true);
			const params = {
				EventId: invite.uid || '',
				CalendarId: view.CalDavCalendarId() || 'default',
				Mode: recurrenceId ? 'occurrence' : 'series'
			};
			if (recurrenceId) {
				params.RecurrenceId = recurrenceId;
			}
			request('RemoveCalendarEvent', params).then(result => {
				view.CalDavBusy(false);
				if (result && result.success) {
					view.CalDavStatus(t('REMOVED', 'Removed from calendar'));
				} else {
					showError(result && result.error);
				}
			}).catch(err => {
				view.CalDavBusy(false);
				showError(err && err.message);
			});
		};

		const message = recurrenceId
			? t('REMOVE_OCCURRENCE_CONFIRM', 'Remove this occurrence from your calendar?')
			: t('REMOVE_CONFIRM', 'Remove this event from your calendar?');
		if (window.rl && rl.app && rl.app.ask && typeof rl.app.ask.showModal === 'function') {
			rl.app.ask.showModal([message, doRemove, null]);
		} else if (window.confirm(message)) {
			doRemove();
		}
	};

	view.message.subscribe(msg => {
		// Reset state for the newly shown message
		view.CalDavInvite(null);
		view.CalDavStatus('');
		view.CalDavError('');
		view.CalDavAdded(false);
		view.CalDavLocationUrl('');
		view.CalDavCanAdd(true);
		view.CalDavCanRespond(false);
		view.CalDavCanRemove(false);

		if (!msg) {
			return;
		}

		const attachments = ko.unwrap(msg.attachments) || [];
		const ics = attachments.find(attachment =>
			attachment && 'text/calendar' === attachment.mimeType && attachment.download);
		if (!ics) {
			return;
		}

		rl.fetch(ics.linkDownload())
			.then(response => response.ok ? response.text() : Promise.reject(new Error(t('REQUEST_FAILED', 'Request failed'))))
			.then(text => {
				const invite = parseInvite(text);
				if (!invite || !invite.hasEvent) {
					return;
				}
				invite.rawText = text;
				invite.uid = prop(invite, 'UID', '');
				invite.summary = unescapeText(prop(invite, 'SUMMARY', t('UNTITLED', 'Untitled')));
				invite.organizer = mailAddress(prop(invite, 'ORGANIZER', ''));
				const location = splitLocation(prop(invite, 'LOCATION', ''));
				invite.location = location.location;
				invite.meetingUrl = location.meetingUrl;
				view.CalDavLocationUrl(osmLinkFor(invite.location));
				const tzidStart = propTzid(invite, 'DTSTART');
				const tzidEnd = propTzid(invite, 'DTEND') || tzidStart;
				invite.start = formatIcsDate(prop(invite, 'DTSTART', ''), tzidStart);
				invite.end = formatIcsDate(prop(invite, 'DTEND', ''), tzidEnd);
				invite.recurrence = recurrenceText(invite);
				invite.recurrenceId = prop(invite, 'RECURRENCE-ID', '');

				const isCancel = 'CANCEL' === invite.method;
				view.CalDavCanAdd(!isCancel);
				view.CalDavCanRespond(!!invite.organizer && !isCancel);
				view.CalDavCanRemove(isCancel && !!invite.uid);
				view.CalDavInvite(invite);
				loadCalendars();
			})
			.catch(() => {});
	});
});

})(window.rl);
