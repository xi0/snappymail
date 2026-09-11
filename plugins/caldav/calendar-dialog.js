// Mailbux CalDAV Auto - Calendar Button + Dialog
// Adds a `buttonCalendar` button right of `.buttonContacts` in the main view
// and opens a dialog with a multi-calendar selector and Day/Week/Month views.
(() => {
'use strict';

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
// Display order for week/month views (Monday first)
const WEEKDAYS_MON = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June',
	'July', 'August', 'September', 'October', 'November', 'December'];
const HOUR_PX = 44;

const state = {
	calendars: [],
	selected: new Set(),
	events: [],
	view: 'month',
	cursor: startOfDay(new Date()),
	loading: false,
	error: ''
};

let dialogEl = null;

/* ------------------------------------------------------------------ utils */

function t(key, fallback) {
	try {
		const v = (window.rl && rl.i18n) ? rl.i18n(key) : key;
		return (v && v !== key) ? v : fallback;
	} catch (e) {
		return fallback;
	}
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
	return date.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'});
}

function shortDate(date) {
	return MONTHS[date.getMonth()].slice(0, 3) + ' ' + date.getDate();
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

/* --------------------------------------------------------------- requests */

function request(action, params) {
	return new Promise((resolve, reject) => {
		if (!window.rl || typeof rl.pluginRemoteRequest !== 'function') {
			reject(new Error('Remote not available'));
			return;
		}
		rl.pluginRemoteRequest((iError, oData) => {
			if (iError || !oData || !oData.Result) {
				reject(new Error((oData && (oData.message || oData.error)) || 'Request failed'));
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
	btn.title = t('SETTINGS_FOLDERS/TYPE_CALENDAR', 'Calendar');
	if (!btn.getAttribute('data-i18n')) {
		btn.setAttribute('data-i18n', '[title]SETTINGS_FOLDERS/TYPE_CALENDAR');
	}
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
				<button type="button" class="mc-close" data-cal-close aria-label="Close">×</button>
			</div>
			<div class="mc-body">
				<aside class="mc-sidebar">
					<div class="mc-sidebar-title">Calendars</div>
					<div class="mc-cal-list"></div>
				</aside>
				<div class="mc-content"></div>
			</div>
		</div>`;

	dialogEl.querySelector('.mc-title-text').textContent = t('SETTINGS_FOLDERS/TYPE_CALENDAR', 'Calendar');

	dialogEl.addEventListener('click', event => {
		const target = event.target;
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
		closeDialog();
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
	}
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
			calendars = [{id: 'default', name: 'Calendar', color: '#00639a'}];
		}
		state.calendars = calendars;
		state.selected = new Set(calendars.map(c => c.id));
		await loadEvents();
	} catch (e) {
		state.error = (e && e.message) || 'Failed to load calendars';
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
}

function normalizeEvent(raw, calendar) {
	const start = parseDate(raw.dtstart || raw.start);
	if (!start) {
		return null;
	}
	return {
		id: raw.uid || ('event-' + Math.random().toString(36).slice(2)),
		calendarId: calendar.id,
		title: raw.summary || 'Untitled',
		start: start,
		end: parseDate(raw.dtend || raw.end),
		allDay: !!raw.allDay,
		location: raw.location || '',
		description: raw.description || '',
		color: calendar.color || '#00639a'
	};
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
		list.innerHTML = '<div class="mc-sidebar-empty">' + (state.loading ? 'Loading…' : 'No calendars') + '</div>';
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
		content.innerHTML = '<div class="mc-info"><div class="mc-spinner"></div><p>Loading…</p></div>';
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
		return MONTHS[cursor.getMonth()] + ' ' + cursor.getFullYear();
	}
	if (state.view === 'day') {
		return WEEKDAYS[cursor.getDay()] + ', ' + MONTHS[cursor.getMonth()] + ' ' + cursor.getDate() + ', ' + cursor.getFullYear();
	}
	const start = startOfWeek(cursor);
	return shortDate(start) + ' – ' + shortDate(addDays(start, 6)) + ', ' + addDays(start, 6).getFullYear();
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
		html += '<div class="' + classes.join(' ') + '">';
		html += '<div class="mc-day-num">' + day.getDate() + '</div>';
		html += '<div class="mc-day-events">';
		dayEvents.slice(0, 3).forEach(event => {
			html += '<div class="mc-event" style="background-color:' + esc(event.color) + '" title="' + esc(eventTooltip(event)) + '">'
				+ (event.allDay ? '' : '<span class="mc-event-time">' + timeLabel(event.start) + '</span> ')
				+ esc(event.title) + '</div>';
		});
		if (dayEvents.length > 3) {
			html += '<div class="mc-event-more">+' + (dayEvents.length - 3) + ' more</div>';
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
		html += '<div class="mc-timeslot"><span>' + (h ? String(h).padStart(2, '0') + ':00' : '') + '</span></div>';
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
			html += '<span class="mc-daycol-num">' + MONTHS[day.getMonth()] + ' ' + day.getDate() + ', ' + day.getFullYear() + '</span>';
		} else {
			html += '<span class="mc-daycol-name">' + WEEKDAYS[day.getDay()] + '</span>';
			html += '<span class="mc-daycol-num">' + day.getDate() + '</span>';
		}
		html += '</div>';
		html += '<div class="mc-daycol-allday">';
		allDay.forEach(event => {
			html += '<div class="mc-event mc-event-allday" style="background-color:' + esc(event.color) + '" title="' + esc(eventTooltip(event)) + '">'
				+ esc(event.title) + '</div>';
		});
		html += '</div>';
		html += '</div>';
		html += '<div class="mc-daycol-body">';
		timed.forEach(event => {
			const position = eventPosition(event, day);
			if (position) {
				html += '<div class="mc-event mc-event-timed" title="' + esc(eventTooltip(event)) + '"'
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
