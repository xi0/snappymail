// Mailbux CalDAV Auto - Calendar invite (received as .ics attachment)
// When a message contains a text/calendar attachment, show an invite box in the
// message view that lets the user add the event to their calendar and/or respond
// to the invitation (Accept / Tentative / Decline). All calendar work is done by
// the caldav plugin backend (ImportCalendarEvent / RespondToEvent JSON actions).
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
	return ('' + value)
		.replace(/\\n/gi, '\n')
		.replace(/\\,/g, ',')
		.replace(/\\;/g, ';')
		.replace(/\\\\/g, '\\');
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

// Format an iCalendar date (20251106T143000Z / 20251106) for display.
function formatIcsDate(value) {
	const str = '' + (value || '');
	let m = str.match(/^(\d{4})(\d{2})(\d{2})(?:T(\d{2})(\d{2})(\d{2}))?(Z)?$/);
	if (!m) {
		return str;
	}
	try {
		const date = m[4]
			? new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]))
			: new Date(+m[1], +m[2] - 1, +m[3]);
		const options = m[4]
			? {dateStyle: 'medium', timeStyle: 'short'}
			: {dateStyle: 'full'};
		if (m[7] && typeof Date.prototype.format === 'function') {
			return date.format({dateStyle: 'medium', timeStyle: 'short'});
		}
		return (typeof date.format === 'function')
			? date.format(options)
			: date.toLocaleString(undefined, options);
	} catch (e) {
		return str;
	}
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
							<td data-bind="text: CalDavInvite() && CalDavInvite().location"></td>
						</tr>
					</tbody></table>
					<div class="caldavInviteActions">
						<select class="caldavInviteCalendar"
							data-bind="visible: CalDavCalendars().length > 1, options: CalDavCalendars, optionsText: 'name', optionsValue: 'id', value: CalDavCalendarId"></select>
						<button type="button" class="btn btn-success caldavInviteAdd"
							data-bind="click: caldavAddEvent, disable: CalDavBusy(), text: caldavAddText"></button>
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
	view.CalDavCanRespond = ko.observable(false);
	view.CalDavBusy = ko.observable(false);
	view.CalDavStatus = ko.observable('');
	view.CalDavError = ko.observable('');
	view.CalDavAdded = ko.observable(false);
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
				Ics: invite.rawText,
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
			Uid: invite.uid || ''
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

	view.message.subscribe(msg => {
		// Reset state for the newly shown message
		view.CalDavInvite(null);
		view.CalDavStatus('');
		view.CalDavError('');
		view.CalDavAdded(false);
		view.CalDavCanRespond(false);

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
				invite.location = unescapeText(prop(invite, 'LOCATION', ''));
				invite.start = formatIcsDate(prop(invite, 'DTSTART', ''));
				invite.end = formatIcsDate(prop(invite, 'DTEND', ''));

				view.CalDavCanRespond(!!invite.organizer && 'CANCEL' !== invite.method);
				view.CalDavInvite(invite);
				loadCalendars();
			})
			.catch(() => {});
	});
});

})(window.rl);
