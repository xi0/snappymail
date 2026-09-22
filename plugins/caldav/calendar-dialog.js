// Mailbux CalDAV Auto - Calendar Button + Dialog
// Adds a `buttonCalendar` button right of `.buttonContacts` in the main view
// and opens a dialog with a multi-calendar selector and Day/Week/Month views.
(() => {
'use strict';

// English defaults; actual labels are resolved through rl.i18n (CALDAV namespace)
// so they follow the user's SnappyMail language (Danish or English).
const DEFAULT_WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const HOUR_PX = 44;

// SnappyMail exposes the configured locale (documentElement.lang /
// dataset.dateLang) and the user's hour format (the `hourCycle` setting).
// Reuse the exact same formatting helper SnappyMail core uses so calendar
// dates and times are rendered like the rest of the webmail.
function snappyLocale() {
	const el = document.documentElement;
	return (el && (el.dataset.dateLang || el.lang)) || undefined;
}

function snappyHourCycle() {
	try {
		return (window.rl && rl.settings && rl.settings.get) ? (rl.settings.get('hourCycle') || '') : '';
	} catch (e) {
		return '';
	}
}

// Mirrors SnappyMail's Date.prototype.format()/timestampToString() behaviour.
function formatDate(date, options) {
	try {
		if (typeof Date.prototype.format === 'function') {
			return date.format(options, 0, snappyHourCycle());
		}
		const opts = Object.assign({}, options);
		const hourCycle = snappyHourCycle();
		if (hourCycle) {
			opts.hourCycle = hourCycle;
		}
		return date.toLocaleString(snappyLocale(), opts);
	} catch (e) {
		// Some engines reject certain hourCycle values - fall back to plain locale formatting
		return (options.hour || options.minute)
			? date.toLocaleTimeString(snappyLocale(), options)
			: date.toLocaleDateString(snappyLocale(), options);
	}
}

// Localized weekday/month names, following SnappyMail's language. Falls back to
// the plugin's own translations when Intl is not available.
function localizedWeekdayNames() {
	return DEFAULT_WEEKDAYS.map((name, index) => {
		try {
			// 2024-01-07 is a Sunday
			return new Intl.DateTimeFormat(snappyLocale(), {weekday: 'short'}).format(new Date(2024, 0, 7 + index));
		} catch (e) {
			return t('CALDAV/WEEKDAY_' + index, name);
		}
	});
}

const WEEKDAYS = localizedWeekdayNames();
// Display order for week/month views (Monday first)
const WEEKDAYS_MON = [1, 2, 3, 4, 5, 6, 0].map(index => WEEKDAYS[index]);

// The selected view is remembered per browser so the calendar reopens with the
// user's last choice. Week is the default when nothing has been stored yet.
const VIEW_STORAGE_KEY = 'caldav.view';
const VIEW_DEFAULT = 'week';
const VIEWS = ['day', 'week', 'month'];

const state = {
	calendars: [],
	selected: new Set(),
	events: [],
	view: loadSavedView(),
	cursor: startOfDay(new Date()),
	loading: false,
	error: ''
};

let dialogEl = null;

/* ------------------------------------------------------------------ utils */

function loadSavedView() {
	try {
		const view = window.localStorage.getItem(VIEW_STORAGE_KEY);
		return VIEWS.includes(view) ? view : VIEW_DEFAULT;
	} catch (e) {
		// Storage unavailable (private mode / disabled) - fall back to the default
		return VIEW_DEFAULT;
	}
}

function saveView(view) {
	try {
		if (VIEWS.includes(view)) {
			window.localStorage.setItem(VIEW_STORAGE_KEY, view);
		}
	} catch (e) {
		// Ignore storage errors; the view still works for the current session
	}
}

function t(key, fallback, params) {
	let value = '';
	try {
		value = (window.rl && rl.i18n) ? rl.i18n(key) : '';
	} catch (e) {
		value = '';
	}
	if (!value || value === key) {
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

function startOfDay(date) {
	return new Date(date.getFullYear(), date.getMonth(), date.getDate());
}

function startOfWeek(date) {
	// Monday is the first day of the week
	const d = startOfDay(date);
	const offset = (d.getDay() + 6) % 7;
	d.setDate(d.getDate() - offset);
	return d;
}

function addDays(date, amount) {
	const d = new Date(date.getTime());
	d.setDate(d.getDate() + amount);
	return d;
}

function sameDay(a, b) {
	return a.getFullYear() === b.getFullYear()
		&& a.getMonth() === b.getMonth()
		&& a.getDate() === b.getDate();
}

function timeLabel(date) {
	// SnappyMail's "LT" format ({hour:'numeric', minute:'numeric'}) + hourCycle
	return formatDate(date, {hour: 'numeric', minute: 'numeric'});
}

// Time-axis label for a given hour (0-23), following SnappyMail's time format
function hourLabel(hour) {
	return formatDate(new Date(2024, 0, 1, hour, 0), {hour: 'numeric', minute: 'numeric'});
}

function shortDate(date) {
	return formatDate(date, {month: 'short', day: 'numeric'});
}

function eventEnd(event) {
	if (event.allDay) {
		const s = startOfDay(event.start);
		let e = event.end ? startOfDay(event.end) : null;
		if (!e || e <= s) {
			e = addDays(s, 1);
		}
		return e;
	}
	return (event.end && event.end > event.start) ? event.end : event.start;
}

function parseDate(value) {
	if (!value) {
		return null;
	}
	if (value instanceof Date) {
		return isNaN(value.getTime()) ? null : value;
	}
	const str = '' + value;
	// Date only (all-day) -> interpret in local time to avoid timezone shifts
	const m = str.match(/^(\d{4})-(\d{2})-(\d{2})$/);
	if (m) {
		return new Date(+m[1], +m[2] - 1, +m[3]);
	}
	const d = new Date(str);
	return isNaN(d.getTime()) ? null : d;
}

function pad2(value) {
	return String(value).padStart(2, '0');
}

// Format a Date as the value of an <input type="date"> (local time)
function dateInputValue(date) {
	return date.getFullYear() + '-' + pad2(date.getMonth() + 1) + '-' + pad2(date.getDate());
}

// Format a Date as the value of an <input type="datetime-local"> (local time)
function timeInputValue(date) {
	return dateInputValue(date) + 'T' + pad2(date.getHours()) + ':' + pad2(date.getMinutes());
}

// Parse an <input type="date"> value into a local midnight Date
function parseInputDate(value) {
	const m = ('' + value).match(/^(\d{4})-(\d{2})-(\d{2})$/);
	return m ? new Date(+m[1], +m[2] - 1, +m[3]) : null;
}

// Parse an <input type="datetime-local"> value into a local Date
function parseInputDateTime(value) {
	const d = new Date(value);
	return isNaN(d.getTime()) ? null : d;
}

// Next full hour (used as the default start for a new timed event)
function nextHour() {
	const now = new Date();
	return new Date(now.getFullYear(), now.getMonth(), now.getDate(), now.getHours() + 1, 0, 0, 0);
}

/* --------------------------------------------------------------- requests */

function request(action, params) {
	return new Promise((resolve, reject) => {
		if (!window.rl || typeof rl.pluginRemoteRequest !== 'function') {
			reject(new Error(t('CALDAV/REMOTE_NOT_AVAILABLE', 'Remote not available')));
			return;
		}
		rl.pluginRemoteRequest((iError, oData) => {
			if (iError || !oData || !oData.Result) {
				reject(new Error((oData && (oData.message || oData.error)) || t('CALDAV/REQUEST_FAILED', 'Request failed')));
			} else {
				resolve(oData.Result);
			}
		}, action, params || {});
	});
}

/* ------------------------------------------------------------------ button */

function setupButton() {
	// Prefer the button rendered by the main view template
	let btn = document.querySelector('.buttonCalendar');

	// Fallback: inject it right of the contacts button
	if (!btn) {
		const contacts = document.querySelector('.buttonContacts');
		if (!contacts) {
			return;
		}
		btn = document.createElement('a');
		btn.href = '#';
		btn.className = 'btn buttonCalendar fontastic';
		btn.setAttribute('role', 'button');
		btn.textContent = '📅';
		contacts.insertAdjacentElement('afterend', btn);
	}

	if (btn.dataset.calendarBound) {
		return;
	}
	btn.dataset.calendarBound = '1';
	// Use the plugin's own key so the tooltip follows the language too
	// (the core template ships this button with an English-only core key).
	btn.title = t('CALDAV/CALENDAR', 'Calendar');
	btn.setAttribute('data-i18n', '[title]CALDAV/CALENDAR');
	btn.addEventListener('click', event => {
		event.preventDefault();
		openDialog();
	});
}

/* ------------------------------------------------------------------ dialog */

function buildDialog() {
	dialogEl = document.createElement('div');
	dialogEl.className = 'mailbux-cal-dialog';
	dialogEl.setAttribute('role', 'dialog');
	dialogEl.setAttribute('aria-modal', 'true');
	dialogEl.innerHTML = `
		<div class="mc-overlay" data-cal-close></div>
		<div class="mc-window">
			<div class="mc-topbar">
				<div class="mc-title"><span class="mc-title-icon">📅</span><span class="mc-title-text"></span></div>
				<div class="mc-nav">
					<button type="button" class="mc-btn" data-cal-nav="prev" title="Previous">‹</button>
					<button type="button" class="mc-btn" data-cal-nav="today">Today</button>
					<button type="button" class="mc-btn" data-cal-nav="next" title="Next">›</button>
				</div>
				<div class="mc-period"></div>
				<div class="mc-views">
					<button type="button" class="mc-btn" data-cal-view="day">Day</button>
					<button type="button" class="mc-btn" data-cal-view="week">Week</button>
					<button type="button" class="mc-btn" data-cal-view="month">Month</button>
				</div>
				<button type="button" class="mc-btn mc-new" data-cal-new>+</button>
				<button type="button" class="mc-close" data-cal-close aria-label="Close">×</button>
			</div>
			<div class="mc-body">
				<aside class="mc-sidebar">
					<div class="mc-sidebar-title">Calendars</div>
					<div class="mc-cal-list"></div>
				</aside>
				<div class="mc-content"></div>
			</div>
			<div class="mc-form" data-cal-form hidden>
				<div class="mc-form-overlay" data-cal-form-close></div>
				<form class="mc-form-window" autocomplete="off" novalidate>
					<div class="mc-form-head">
						<span class="mc-form-title"></span>
						<button type="button" class="mc-close" data-cal-form-close aria-label="Close">×</button>
					</div>
					<div class="mc-form-body">
						<label class="mc-field">
							<span class="mc-field-label mc-lbl-title"></span>
							<input type="text" name="title" maxlength="255" required>
						</label>
						<label class="mc-field">
							<span class="mc-field-label mc-lbl-calendar"></span>
							<select name="calendar"></select>
						</label>
						<label class="mc-check">
							<input type="checkbox" name="allday">
							<span class="mc-lbl-allday"></span>
						</label>
						<div class="mc-field-row">
							<label class="mc-field">
								<span class="mc-field-label mc-lbl-start"></span>
								<input type="date" name="startDate">
								<input type="datetime-local" name="startTime">
							</label>
							<label class="mc-field">
								<span class="mc-field-label mc-lbl-end"></span>
								<input type="date" name="endDate">
								<input type="datetime-local" name="endTime">
							</label>
						</div>
						<label class="mc-field">
							<span class="mc-field-label mc-lbl-location"></span>
							<input type="text" name="location" maxlength="255">
						</label>
						<label class="mc-field">
							<span class="mc-field-label mc-lbl-description"></span>
							<textarea name="description" rows="3"></textarea>
						</label>
						<div class="mc-form-error" hidden></div>
						<div class="mc-form-actions">
							<button type="button" class="mc-btn mc-btn-danger" data-cal-delete hidden></button>
							<span class="mc-form-spacer"></span>
							<button type="button" class="mc-btn" data-cal-cancel></button>
							<button type="submit" class="mc-btn mc-btn-primary" data-cal-save></button>
						</div>
					</div>
				</form>
			</div>
		</div>`;

	const q = selector => dialogEl.querySelector(selector);
	q('[data-cal-nav="prev"]').title = t('CALDAV/PREVIOUS', 'Previous');
	q('[data-cal-nav="today"]').textContent = t('CALDAV/TODAY', 'Today');
	q('[data-cal-nav="next"]').title = t('CALDAV/NEXT', 'Next');
	q('[data-cal-view="day"]').textContent = t('CALDAV/DAY', 'Day');
	q('[data-cal-view="week"]').textContent = t('CALDAV/WEEK', 'Week');
	q('[data-cal-view="month"]').textContent = t('CALDAV/MONTH', 'Month');
	q('.mc-sidebar-title').textContent = t('CALDAV/CALENDARS', 'Calendars');
	q('.mc-close').setAttribute('aria-label', t('CALDAV/CLOSE', 'Close'));
	q('.mc-title-text').textContent = t('CALDAV/CALENDAR', 'Calendar');

	// "New event" button
	q('[data-cal-new]').textContent = '+';
	q('[data-cal-new]').title = t('CALDAV/NEW_EVENT', 'New event');
	q('[data-cal-new]').setAttribute('aria-label', t('CALDAV/NEW_EVENT', 'New event'));

	// Event form labels / buttons
	q('.mc-lbl-title').textContent = t('CALDAV/TITLE', 'Title');
	q('.mc-lbl-calendar').textContent = t('CALDAV/CALENDAR', 'Calendar');
	q('.mc-lbl-allday').textContent = t('CALDAV/ALL_DAY', 'All day');
	q('.mc-lbl-start').textContent = t('CALDAV/START', 'Start');
	q('.mc-lbl-end').textContent = t('CALDAV/END', 'End');
	q('.mc-lbl-location').textContent = t('CALDAV/LOCATION', 'Location');
	q('.mc-lbl-description').textContent = t('CALDAV/DESCRIPTION', 'Description');
	q('[data-cal-delete]').textContent = t('CALDAV/DELETE', 'Delete');
	q('[data-cal-cancel]').textContent = t('CALDAV/CANCEL', 'Cancel');
	q('[data-cal-save]').textContent = t('CALDAV/SAVE', 'Save');
	q('.mc-form-title').textContent = t('CALDAV/NEW_EVENT', 'New event');
	q('.mc-form-head .mc-close').setAttribute('aria-label', t('CALDAV/CLOSE', 'Close'));
	q('.mc-form-window').addEventListener('submit', saveEventForm);
	q('[name="allday"]').addEventListener('change', () => toggleFormAllDay(eventFormEls()));
	q('[data-cal-delete]').addEventListener('click', deleteEventForm);

	dialogEl.addEventListener('click', event => {
		const target = event.target;
		// Event form interactions take priority
		if (target.closest('[data-cal-form-close]') || target.closest('[data-cal-cancel]')) {
			closeEventForm();
			return;
		}
		if (target.closest('[data-cal-new]')) {
			openEventForm(null);
			return;
		}
		if (target.closest('[data-cal-form]')) {
			// Ignore clicks inside the form itself
			return;
		}
		// Clicking an existing event opens the edit form
		const eventEl = target.closest('[data-cal-event]');
		if (eventEl) {
			const chosen = state.events.find(item => item.domId === eventEl.dataset.calEvent);
			if (chosen) {
				openEventForm(chosen);
			}
			return;
		}
		// The "+N more" label should not open the form
		if (target.closest('.mc-event-more')) {
			return;
		}
		// Clicking an empty month cell creates an all-day event on that date
		const newDateEl = target.closest('[data-cal-newdate]');
		if (newDateEl) {
			const day = parseInputDate(newDateEl.dataset.calNewdate);
			if (day) {
				openEventForm(null, {allDay: true, start: day, calendarId: defaultCalendarId()});
			}
			return;
		}
		// Clicking an empty time-grid column creates a timed event at that slot
		const newSlotEl = target.closest('[data-cal-newslot]');
		if (newSlotEl) {
			const day = parseInputDate(newSlotEl.dataset.calNewslot);
			if (day) {
				const rect = newSlotEl.getBoundingClientRect();
				let minutes = ((event.clientY - rect.top) / HOUR_PX) * 60;
				minutes = Math.max(0, Math.min(23 * 60, Math.round(minutes / 30) * 30));
				const start = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 0, minutes, 0, 0);
				openEventForm(null, {allDay: false, start: start, calendarId: defaultCalendarId()});
			}
			return;
		}

		if (target.closest('[data-cal-close]')) {
			closeDialog();
			return;
		}
		const nav = target.closest('[data-cal-nav]');
		if (nav) {
			navigate(nav.dataset.calNav);
			return;
		}
		const view = target.closest('[data-cal-view]');
		if (view) {
			state.view = view.dataset.calView;
			saveView(state.view);
			renderContent();
			return;
		}
	});

	dialogEl.querySelector('.mc-cal-list').addEventListener('change', event => {
		const input = event.target.closest('input[data-cal-id]');
		if (!input) {
			return;
		}
		if (input.checked) {
			state.selected.add(input.dataset.calId);
		} else {
			state.selected.delete(input.dataset.calId);
		}
		renderContent();
	});

	document.body.appendChild(dialogEl);
}

function onKeydown(event) {
	if (event.key === 'Escape') {
		const form = dialogEl && dialogEl.querySelector('[data-cal-form]');
		if (form && !form.hidden) {
			closeEventForm();
		} else {
			closeDialog();
		}
	}
}

function openDialog() {
	if (!dialogEl) {
		buildDialog();
	}
	dialogEl.classList.add('show');
	document.addEventListener('keydown', onKeydown, true);
	state.error = '';

	if (!state.calendars.length) {
		loadCalendars();
	} else {
		renderSidebar();
		renderContent();
		refreshEvents();
	}
}

function closeDialog() {
	if (dialogEl) {
		dialogEl.classList.remove('show');
		const form = dialogEl.querySelector('[data-cal-form]');
		if (form) {
			form.hidden = true;
		}
	}
	editingEvent = null;
	document.removeEventListener('keydown', onKeydown, true);
}

/* -------------------------------------------------------------------- data */

async function loadCalendars() {
	state.loading = true;
	state.error = '';
	renderSidebar();
	renderContent();
	try {
		const result = await request('GetCalendars', {});
		let calendars = (result && result.calendars) || [];
		if (!calendars.length) {
			calendars = [{id: 'default', name: t('CALDAV/CALENDAR', 'Calendar'), color: '#00639a'}];
		}
		state.calendars = calendars;
		state.selected = new Set(calendars.map(c => c.id));
		await loadEvents();
	} catch (e) {
		state.error = (e && e.message) || t('CALDAV/FAILED_LOAD_CALENDARS', 'Failed to load calendars');
	}
	state.loading = false;
	renderSidebar();
	renderContent();
}

async function refreshEvents() {
	state.loading = true;
	renderContent();
	await loadEvents();
	state.loading = false;
	renderContent();
}

async function loadEvents() {
	const events = [];
	await Promise.all(state.calendars.map(async calendar => {
		try {
			const result = await request('GetCalendarEvents', {CalendarId: calendar.id});
			((result && result.events) || []).forEach(raw => {
				const event = normalizeEvent(raw, calendar);
				event && events.push(event);
			});
		} catch (e) {
			// Ignore a single failing calendar so the others still show
		}
	}));
	state.events = events;
	// Give each event a stable DOM key for the click handlers
	state.events.forEach((event, index) => {
		event.domId = 'cal-event-' + index;
	});
}

function normalizeEvent(raw, calendar) {
	const start = parseDate(raw.dtstart || raw.start);
	if (!start) {
		return null;
	}
	return {
		id: raw.uid || ('event-' + Math.random().toString(36).slice(2)),
		url: raw.href || '',
		calendarId: calendar.id,
		title: raw.summary || t('CALDAV/UNTITLED', 'Untitled'),
		start: start,
		end: parseDate(raw.dtend || raw.end),
		allDay: !!raw.allDay,
		location: raw.location || '',
		description: raw.description || '',
		color: calendar.color || '#00639a'
	};
}

/* -------------------------------------------------------------- event form */

// The event currently being edited (null when creating a new event)
let editingEvent = null;

function eventFormEls() {
	const root = dialogEl.querySelector('[data-cal-form]');
	return {
		root: root,
		title: root.querySelector('[name="title"]'),
		calendar: root.querySelector('[name="calendar"]'),
		allday: root.querySelector('[name="allday"]'),
		startDate: root.querySelector('[name="startDate"]'),
		startTime: root.querySelector('[name="startTime"]'),
		endDate: root.querySelector('[name="endDate"]'),
		endTime: root.querySelector('[name="endTime"]'),
		location: root.querySelector('[name="location"]'),
		description: root.querySelector('[name="description"]'),
		error: root.querySelector('.mc-form-error'),
		del: root.querySelector('[data-cal-delete]'),
		save: root.querySelector('[data-cal-save]')
	};
}

function fillCalendarOptions(select, selectedId) {
	select.innerHTML = state.calendars.map(calendar =>
		'<option value="' + esc(calendar.id) + '"'
		+ (calendar.id === selectedId ? ' selected' : '') + '>'
		+ esc(calendar.name) + '</option>'
	).join('');
}

// Default calendar for a new event: first visible/selected calendar, else the first one
function defaultCalendarId() {
	const visible = state.calendars.find(calendar => state.selected.has(calendar.id));
	if (visible) {
		return visible.id;
	}
	return state.calendars.length ? state.calendars[0].id : 'default';
}

function toggleFormAllDay(el) {
	const allDay = el.allday.checked;
	el.startDate.style.display = allDay ? '' : 'none';
	el.endDate.style.display = allDay ? '' : 'none';
	el.startTime.style.display = allDay ? 'none' : '';
	el.endTime.style.display = allDay ? 'none' : '';
}

function showFormError(el, message) {
	el.error.textContent = message;
	el.error.hidden = !message;
}

// Open the form. Pass an existing event to edit it, or (null, defaults) to create.
function openEventForm(event, defaults) {
	if (!dialogEl) {
		return;
	}
	if (!state.calendars.length) {
		state.error = t('CALDAV/NO_CALENDARS', 'No calendars');
		renderContent();
		return;
	}

	const el = eventFormEls();
	const editing = !!event;
	editingEvent = event || null;
	showFormError(el, '');

	el.root.querySelector('.mc-form-title').textContent = editing
		? t('CALDAV/EDIT_EVENT', 'Edit event')
		: t('CALDAV/NEW_EVENT', 'New event');
	el.del.hidden = !editing;

	if (editing) {
		fillCalendarOptions(el.calendar, event.calendarId);
		el.title.value = event.title || '';
		el.location.value = event.location || '';
		el.description.value = event.description || '';
		el.allday.checked = !!event.allDay;
		const start = event.start || new Date();
		const end = eventEnd(event);
		el.startTime.value = timeInputValue(start);
		el.endTime.value = timeInputValue(end && end > start ? end : new Date(start.getTime() + 3600000));
		el.startDate.value = dateInputValue(start);
		if (event.allDay) {
			// DTEND from the server is exclusive; show an inclusive end date
			let inclusiveEnd = start;
			if (end && end > start) {
				inclusiveEnd = addDays(end, -1);
				if (inclusiveEnd < start) {
					inclusiveEnd = start;
				}
			}
			el.endDate.value = dateInputValue(inclusiveEnd);
		} else {
			el.endDate.value = dateInputValue(end && end > start ? end : start);
		}
	} else {
		const d = defaults || {};
		const allDay = !!d.allDay;
		const start = d.start || nextHour();
		const end = d.end || (allDay ? addDays(start, 1) : new Date(start.getTime() + 3600000));
		fillCalendarOptions(el.calendar, d.calendarId || defaultCalendarId());
		el.title.value = '';
		el.location.value = '';
		el.description.value = '';
		el.allday.checked = allDay;
		el.startTime.value = timeInputValue(start);
		el.endTime.value = timeInputValue(allDay ? addDays(start, 1) : end);
		el.startDate.value = dateInputValue(start);
		// All-day end is inclusive in the UI, exclusive internally
		el.endDate.value = dateInputValue(allDay ? addDays(end, -1) : end);
	}

	toggleFormAllDay(el);
	el.root.hidden = false;
	setTimeout(() => el.title.focus(), 20);
}

function closeEventForm() {
	if (dialogEl) {
		const root = dialogEl.querySelector('[data-cal-form]');
		if (root) {
			root.hidden = true;
		}
	}
	editingEvent = null;
}

async function saveEventForm(event) {
	if (event) {
		event.preventDefault();
	}
	const el = eventFormEls();
	const title = el.title.value.trim();
	if (!title) {
		showFormError(el, t('CALDAV/ERROR_TITLE_REQUIRED', 'Event title required'));
		return;
	}

	const allDay = el.allday.checked;
	const calendarId = el.calendar.value || defaultCalendarId();
	let startParam;
	let endParam;

	if (allDay) {
		const startDate = parseInputDate(el.startDate.value);
		let endDate = parseInputDate(el.endDate.value);
		if (!startDate) {
			showFormError(el, t('CALDAV/ERROR_DATE_REQUIRED', 'Please choose a start date'));
			return;
		}
		if (!endDate || endDate < startDate) {
			endDate = startDate;
		}
		startParam = dateInputValue(startDate);
		// CalDAV DTEND is exclusive for all-day events
		endParam = dateInputValue(addDays(endDate, 1));
	} else {
		const start = parseInputDateTime(el.startTime.value);
		let end = parseInputDateTime(el.endTime.value);
		if (!start) {
			showFormError(el, t('CALDAV/ERROR_DATE_REQUIRED', 'Please choose a start date'));
			return;
		}
		if (!end || end <= start) {
			end = new Date(start.getTime() + 3600000);
		}
		startParam = start.toISOString();
		endParam = end.toISOString();
	}

	const params = {
		CalendarId: calendarId,
		Title: title,
		Start: startParam,
		End: endParam,
		AllDay: allDay ? 1 : 0,
		Description: el.description.value,
		Location: el.location.value
	};

	showFormError(el, '');
	el.save.disabled = true;
	try {
		if (editingEvent) {
			if (calendarId === editingEvent.calendarId) {
				params.EventId = editingEvent.id;
				params.EventUrl = editingEvent.url || '';
				await saveEventRequest('UpdateCalendarEvent', params);
			} else {
				// Moved to another calendar: create in the target, remove from the source
				await saveEventRequest('CreateCalendarEvent', params);
				await saveEventRequest('DeleteCalendarEvent', {
					EventId: editingEvent.id,
					EventUrl: editingEvent.url || '',
					CalendarId: editingEvent.calendarId
				});
			}
		} else {
			await saveEventRequest('CreateCalendarEvent', params);
		}
		closeEventForm();
		await refreshEvents();
	} catch (e) {
		showFormError(el, (e && e.message) || t('CALDAV/REQUEST_FAILED', 'Request failed'));
	} finally {
		el.save.disabled = false;
	}
}

// Wrap request() and turn a {success:false} payload into a rejection
async function saveEventRequest(action, params) {
	const result = await request(action, params);
	if (result && false === result.success) {
		throw new Error(result.error || t('CALDAV/REQUEST_FAILED', 'Request failed'));
	}
	return result;
}

async function deleteEventForm() {
	if (!editingEvent) {
		return;
	}
	if (!window.confirm(t('CALDAV/DELETE_CONFIRM', 'Delete this event?'))) {
		return;
	}
	const el = eventFormEls();
	showFormError(el, '');
	el.del.disabled = true;
	try {
		await saveEventRequest('DeleteCalendarEvent', {
			EventId: editingEvent.id,
			EventUrl: editingEvent.url || '',
			CalendarId: editingEvent.calendarId
		});
		closeEventForm();
		await refreshEvents();
	} catch (e) {
		showFormError(el, (e && e.message) || t('CALDAV/REQUEST_FAILED', 'Request failed'));
	} finally {
		el.del.disabled = false;
	}
}

/* ------------------------------------------------------------------ render */

function visibleEvents() {
	return state.events.filter(event => state.selected.has(event.calendarId));
}

function eventsOnDay(events, day) {
	const dayStart = startOfDay(day);
	const dayEnd = addDays(dayStart, 1);
	return events
		.filter(event => event.start < dayEnd && eventEnd(event) > dayStart)
		.sort((a, b) => (a.allDay === b.allDay) ? (a.start - b.start) : (a.allDay ? -1 : 1));
}

function eventTooltip(event) {
	let text = (event.allDay ? '' : timeLabel(event.start) + ' ') + event.title;
	if (event.location) {
		text += ' — ' + event.location;
	}
	return text;
}

function renderSidebar() {
	if (!dialogEl) {
		return;
	}
	const list = dialogEl.querySelector('.mc-cal-list');
	if (!state.calendars.length) {
		list.innerHTML = '<div class="mc-sidebar-empty">'
			+ esc(state.loading ? t('CALDAV/LOADING', 'Loading…') : t('CALDAV/NO_CALENDARS', 'No calendars'))
			+ '</div>';
		return;
	}
	list.innerHTML = state.calendars.map(calendar =>
		'<label class="mc-cal-item">'
		+ '<input type="checkbox" data-cal-id="' + esc(calendar.id) + '"'
		+ (state.selected.has(calendar.id) ? ' checked' : '') + '>'
		+ '<span class="mc-cal-dot" style="background-color:' + esc(calendar.color) + '"></span>'
		+ '<span class="mc-cal-name">' + esc(calendar.name) + '</span>'
		+ '</label>'
	).join('');
}

function renderContent() {
	if (!dialogEl) {
		return;
	}
	const content = dialogEl.querySelector('.mc-content');
	const period = dialogEl.querySelector('.mc-period');

	dialogEl.querySelectorAll('[data-cal-view]').forEach(btn =>
		btn.classList.toggle('active', btn.dataset.calView === state.view));
	period.textContent = periodLabel();

	if (state.loading) {
		content.innerHTML = '<div class="mc-info"><div class="mc-spinner"></div><p>'
			+ esc(t('CALDAV/LOADING', 'Loading…'))
			+ '</p></div>';
		return;
	}
	if (state.error) {
		content.innerHTML = '<div class="mc-info mc-error">' + esc(state.error) + '</div>';
		return;
	}

	if (state.view === 'month') {
		renderMonth(content);
	} else if (state.view === 'week') {
		renderWeek(content);
	} else {
		renderDay(content);
	}
}

function periodLabel() {
	const cursor = state.cursor;
	if (state.view === 'month') {
		return formatDate(cursor, {month: 'long', year: 'numeric'});
	}
	if (state.view === 'day') {
		return formatDate(cursor, {weekday: 'long', month: 'long', day: 'numeric', year: 'numeric'});
	}
	const start = startOfWeek(cursor);
	const end = addDays(start, 6);
	return shortDate(start) + ' – ' + shortDate(end) + ', ' + end.getFullYear();
}

function renderMonth(content) {
	const cursor = state.cursor;
	const month = cursor.getMonth();
	const first = new Date(cursor.getFullYear(), month, 1);
	const gridStart = addDays(first, -((first.getDay() + 6) % 7));
	const events = visibleEvents();
	const today = startOfDay(new Date());

	let html = '<div class="mc-month">';
	html += '<div class="mc-month-head">';
	WEEKDAYS_MON.forEach(day => html += '<div class="mc-month-head-cell">' + day + '</div>');
	html += '</div><div class="mc-month-grid">';

	for (let i = 0; i < 42; i++) {
		const day = addDays(gridStart, i);
		const dayEvents = eventsOnDay(events, day);
		const classes = ['mc-day'];
		if (day.getMonth() !== month) {
			classes.push('mc-other-month');
		}
		if (sameDay(day, today)) {
			classes.push('mc-today');
		}
		html += '<div class="' + classes.join(' ') + '" data-cal-newdate="' + dateInputValue(day) + '">';
		html += '<div class="mc-day-num">' + day.getDate() + '</div>';
		html += '<div class="mc-day-events">';
		dayEvents.slice(0, 3).forEach(event => {
			html += '<div class="mc-event" data-cal-event="' + esc(event.domId) + '" style="background-color:' + esc(event.color) + '" title="' + esc(eventTooltip(event)) + '">'
				+ (event.allDay ? '' : '<span class="mc-event-time">' + timeLabel(event.start) + '</span> ')
				+ esc(event.title) + '</div>';
		});
		if (dayEvents.length > 3) {
			html += '<div class="mc-event-more">'
				+ esc(t('CALDAV/MORE', '+%COUNT% more', {COUNT: dayEvents.length - 3}))
				+ '</div>';
		}
		html += '</div></div>';
	}

	html += '</div></div>';
	content.innerHTML = html;
	content.scrollTop = 0;
}

function renderTimeGrid(content, days, singleDay) {
	const events = visibleEvents();
	const today = startOfDay(new Date());

	let html = '<div class="mc-timegrid' + (singleDay ? ' mc-single' : '') + '">';
	html += '<div class="mc-timecol">';
	html += '<div class="mc-timecol-head"></div><div class="mc-timecol-body">';
	for (let h = 0; h < 24; h++) {
		html += '<div class="mc-timeslot"><span>' + (h ? esc(hourLabel(h)) : '') + '</span></div>';
	}
	html += '</div></div>';

	days.forEach(day => {
		const dayEvents = eventsOnDay(events, day);
		const allDay = dayEvents.filter(event => event.allDay);
		const timed = dayEvents.filter(event => !event.allDay);

		html += '<div class="mc-daycol' + (sameDay(day, today) ? ' mc-today' : '') + '">';
		html += '<div class="mc-daycol-head">';
		html += '<div class="mc-daycol-datenum">';
		if (singleDay) {
			html += '<span class="mc-daycol-name">' + WEEKDAYS[day.getDay()] + '</span>';
			html += '<span class="mc-daycol-num">' + formatDate(day, {month: 'long', day: 'numeric', year: 'numeric'}) + '</span>';
		} else {
			html += '<span class="mc-daycol-name">' + WEEKDAYS[day.getDay()] + '</span>';
			html += '<span class="mc-daycol-num">' + day.getDate() + '</span>';
		}
		html += '</div>';
		html += '<div class="mc-daycol-allday" data-cal-newdate="' + dateInputValue(day) + '">';
		allDay.forEach(event => {
			html += '<div class="mc-event mc-event-allday" data-cal-event="' + esc(event.domId) + '" style="background-color:' + esc(event.color) + '" title="' + esc(eventTooltip(event)) + '">'
				+ esc(event.title) + '</div>';
		});
		html += '</div>';
		html += '</div>';
		html += '<div class="mc-daycol-body" data-cal-newslot="' + dateInputValue(day) + '">';
		timed.forEach(event => {
			const position = eventPosition(event, day);
			if (position) {
				html += '<div class="mc-event mc-event-timed" data-cal-event="' + esc(event.domId) + '" title="' + esc(eventTooltip(event)) + '"'
					+ ' style="top:' + position.top + 'px;height:' + position.height + 'px;background-color:' + esc(event.color) + '">'
					+ '<span class="mc-event-time">' + timeLabel(event.start) + '</span>'
					+ '<span class="mc-event-title">' + esc(event.title) + '</span></div>';
			}
		});
		html += '</div></div>';
	});

	html += '</div>';
	content.innerHTML = html;
	content.scrollTop = 8 * HOUR_PX;
}

function renderWeek(content) {
	const start = startOfWeek(state.cursor);
	const days = [];
	for (let i = 0; i < 7; i++) {
		days.push(addDays(start, i));
	}
	renderTimeGrid(content, days, false);
}

function renderDay(content) {
	renderTimeGrid(content, [state.cursor], true);
}

function eventPosition(event, day) {
	const dayStart = startOfDay(day);
	const dayEnd = addDays(dayStart, 1);
	let start = event.start < dayStart ? dayStart : event.start;
	let end = eventEnd(event) > dayEnd ? dayEnd : eventEnd(event);
	if (end <= start) {
		end = new Date(start.getTime() + 30 * 60000);
	}
	const startMin = (start - dayStart) / 60000;
	const endMin = (end - dayStart) / 60000;
	const top = (startMin / 60) * HOUR_PX;
	let height = ((endMin - startMin) / 60) * HOUR_PX;
	if (height < 18) {
		height = 18;
	}
	return {top: Math.round(top), height: Math.round(height)};
}

/* ------------------------------------------------------------- navigation */

function navigate(action) {
	if (action === 'today') {
		state.cursor = startOfDay(new Date());
	} else {
		const step = action === 'next' ? 1 : -1;
		if (state.view === 'month') {
			state.cursor = new Date(state.cursor.getFullYear(), state.cursor.getMonth() + step, 1);
		} else if (state.view === 'week') {
			state.cursor = addDays(state.cursor, 7 * step);
		} else {
			state.cursor = addDays(state.cursor, step);
		}
	}
	renderContent();
}

/* -------------------------------------------------------------------- boot */

let bootstrapTimer = null;

function scheduleInject() {
	if (bootstrapTimer) {
		return;
	}
	bootstrapTimer = setTimeout(() => {
		bootstrapTimer = null;
		setupButton();
	}, 250);
}

function boot() {
	setupButton();
	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', setupButton);
	}
	[500, 1500, 3000].forEach(delay => setTimeout(setupButton, delay));

	if (window.MutationObserver) {
		new MutationObserver(scheduleInject).observe(document.body, {childList: true, subtree: true});
	}
}

// Expose a small API for debugging / integrations
window.MailbuxCalendarDialog = {
	open: openDialog,
	close: closeDialog,
	refresh: () => state.calendars.length && refreshEvents()
};

boot();

})();
