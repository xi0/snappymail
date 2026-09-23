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
	// Occurrences produced by the most recent render (state.events holds series)
	rendered: [],
	view: loadSavedView(),
	cursor: startOfDay(new Date()),
	loading: false,
	error: ''
};

let dialogEl = null;
// Custom date/time pickers used by the event form (built in buildDialog)
let startPicker = null;
let endPicker = null;
let untilPicker = null;
// The series currently being edited (an event on its own, or the master of an occurrence)
let editingSeries = null;

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

// Parse an ISO YYYY-MM-DD value (used by the calendar grid data attributes)
function parseInputDate(value) {
	const m = ('' + value).match(/^(\d{4})-(\d{2})-(\d{2})$/);
	return m ? new Date(+m[1], +m[2] - 1, +m[3]) : null;
}

// Next full hour (used as the default start for a new timed event)
function nextHour() {
	const now = new Date();
	return new Date(now.getFullYear(), now.getMonth(), now.getDate(), now.getHours() + 1, 0, 0, 0);
}

/* ---------------------------------------------------------- recurrence */

// RFC 5545 recurrence expansion (DAILY/WEEKLY/MONTHLY/YEARLY subset) plus
// RDATE/EXDATE and per-occurrence overrides (RECURRENCE-ID).

const DAY_CODES = {SU: 0, MO: 1, TU: 2, WE: 3, TH: 4, FR: 5, SA: 6};

function dayCodeToIndex(code) {
	const key = ('' + (code == null ? '' : code)).replace(/[^A-Za-z]/g, '').toUpperCase();
	return (key in DAY_CODES) ? DAY_CODES[key] : 1;
}

function parseRrule(rrule) {
	const out = {};
	('' + (rrule || '')).split(';').forEach(part => {
		const i = part.indexOf('=');
		if (i > 0) {
			out[part.slice(0, i).toUpperCase()] = part.slice(i + 1);
		}
	});
	return out;
}

// Parse an iCalendar UTC/date instant (YYYYMMDD or YYYYMMDDTHHMMSSZ) to a Date.
function parseIcsInstant(value) {
	const s = ('' + (value == null ? '' : value)).trim();
	const m = s.match(/^(\d{4})(\d{2})(\d{2})(?:T(\d{2})(\d{2})(\d{2})Z?)?$/);
	if (!m) {
		return parseDate(s);
	}
	return m[4]
		? new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]))
		: new Date(+m[1], +m[2] - 1, +m[3]);
}

// nth (1-based, or negative from end) weekday of a month, or null when it does not exist.
function nthWeekdayOfMonth(year, month, dow, nth, ref) {
	const h = ref.getHours();
	const mi = ref.getMinutes();
	const s = ref.getSeconds();
	if (nth > 0) {
		const first = new Date(year, month, 1);
		const day = 1 + ((dow - first.getDay() + 7) % 7) + (nth - 1) * 7;
		const d = new Date(year, month, day, h, mi, s);
		return d.getMonth() === month ? d : null;
	}
	const last = new Date(year, month + 1, 0);
	const day = last.getDate() - ((last.getDay() - dow + 7) % 7) - (Math.abs(nth) - 1) * 7;
	const d = new Date(year, month, day, h, mi, s);
	return d.getMonth() === month ? d : null;
}

// Candidate occurrence start dates for one recurrence period.
function periodCandidates(freq, anchor, period, interval, rule) {
	const list = [];
	if ('DAILY' === freq) {
		list.push(addDays(anchor, period * interval));
	} else if ('WEEKLY' === freq) {
		const base = addDays(startOfWeek(anchor), period * interval * 7);
		const days = rule.BYDAY ? rule.BYDAY.split(',').map(dayCodeToIndex) : [anchor.getDay()];
		days.forEach(dow => list.push(addDays(base, (dow + 6) % 7)));
	} else if ('MONTHLY' === freq) {
		const base = new Date(anchor.getFullYear(), anchor.getMonth() + period * interval, 1);
		if (rule.BYMONTHDAY) {
			rule.BYMONTHDAY.split(',').map(Number).forEach(day => {
				const d = new Date(base.getFullYear(), base.getMonth(), day, anchor.getHours(), anchor.getMinutes(), anchor.getSeconds());
				if (d.getMonth() === base.getMonth()) {
					list.push(d);
				}
			});
		} else if (rule.BYDAY) {
			rule.BYDAY.split(',').forEach(spec => {
				const m = ('' + spec).match(/^([+-]?\d+)?([A-Za-z]{2})$/);
				if (!m) {
					return;
				}
				const nth = m[1] ? parseInt(m[1], 10) : 1;
				const d = nthWeekdayOfMonth(base.getFullYear(), base.getMonth(), dayCodeToIndex(m[2]), nth, anchor);
				if (d) {
					list.push(d);
				}
			});
		} else {
			const d = new Date(base.getFullYear(), base.getMonth(), anchor.getDate(), anchor.getHours(), anchor.getMinutes(), anchor.getSeconds());
			if (d.getMonth() === base.getMonth()) {
				list.push(d);
			}
		}
	} else if ('YEARLY' === freq) {
		const year = anchor.getFullYear() + period * interval;
		list.push(new Date(year, anchor.getMonth(), anchor.getDate(), anchor.getHours(), anchor.getMinutes(), anchor.getSeconds()));
	}
	return list;
}

// Generate occurrence start dates from DTSTART, bounded by the visible range.
function generateOccurrenceStarts(event, rangeEnd) {
	if (!event.rrule) {
		// rdate-only recurrences are handled by the caller
		return [];
	}
	const rule = parseRrule(event.rrule);
	const freq = (rule.FREQ || 'DAILY').toUpperCase();
	const interval = Math.max(1, parseInt(rule.INTERVAL || '1', 10) || 1);
	const until = rule.UNTIL ? parseIcsInstant(rule.UNTIL) : null;
	const count = rule.COUNT ? parseInt(rule.COUNT, 10) : null;

	const anchor = new Date(event.start.getTime());
	const starts = [];
	const seen = new Set();
	let emitted = 0;
	let period = 0;
	let guard = 0;

	while (guard++ < 50000) {
		const candidates = periodCandidates(freq, anchor, period, interval, rule).sort((a, b) => a - b);
		if (candidates.length && candidates[0] > rangeEnd) {
			break;
		}
		for (let k = 0; k < candidates.length; k++) {
			const cand = candidates[k];
			// Day-based candidates (weekly especially) are built from a local
			// midnight, so always restore DTSTART's wall-clock time.
			cand.setHours(anchor.getHours(), anchor.getMinutes(), anchor.getSeconds(), 0);
			if (cand < anchor) {
				continue;
			}
			if (until && cand > until) {
				return starts;
			}
			emitted++;
			if (count && emitted > count) {
				return starts;
			}
			const t = cand.getTime();
			if (!seen.has(t)) {
				seen.add(t);
				starts.push(cand);
			}
		}
		period++;
	}
	return starts;
}

function isRecurringEvent(event) {
	return !!event.rrule || (event.rdate && event.rdate.length > 0);
}

function makeOccurrence(event, start, end, override, recurrenceId) {
	const src = override || event;
	return {
		id: event.id,
		url: event.url,
		calendarId: event.calendarId,
		title: src.title,
		location: src.location,
		description: src.description,
		color: event.color,
		allDay: src.allDay,
		start: start,
		end: end,
		recurrenceId: recurrenceId || null,
		series: event,
		isOverride: !!override,
		recurring: !override && isRecurringEvent(event)
	};
}

// Expand one series into concrete occurrences overlapping [rangeStart, rangeEnd).
function expandSeries(event, rangeStart, rangeEnd) {
	const out = [];
	const duration = Math.max(0, eventEnd(event) - event.start);

	if (!isRecurringEvent(event)) {
		if (eventEnd(event) > rangeStart && event.start < rangeEnd) {
			out.push(makeOccurrence(event, event.start, eventEnd(event), null, null));
		}
		return out;
	}

	const overrideMap = {};
	(event.overrides || []).forEach(o => {
		if (o.recurrenceId) {
			overrideMap[o.recurrenceId.getTime()] = o;
		}
	});
	const exdates = new Set();
	(event.exdate || []).forEach(v => {
		const d = parseDate(v);
		if (d) {
			exdates.add(d.getTime());
		}
	});

	let starts = generateOccurrenceStarts(event, rangeEnd);
	(event.rdate || []).forEach(v => {
		const d = parseDate(v);
		if (d) {
			starts.push(d);
		}
	});

	const seen = new Set();
	starts = starts
		.filter(d => {
			const t = d.getTime();
			if (seen.has(t)) {
				return false;
			}
			seen.add(t);
			return true;
		})
		.sort((a, b) => a - b);

	const usedOverride = new Set();
	starts.forEach(start => {
		const t = start.getTime();
		if (exdates.has(t)) {
			return;
		}
		const ov = overrideMap[t];
		if (ov) {
			usedOverride.add(t);
			if (eventEnd(ov) > rangeStart && ov.start < rangeEnd) {
				out.push(makeOccurrence(event, ov.start, eventEnd(ov), ov, start));
			}
		} else {
			const end = new Date(start.getTime() + duration);
			if (end > rangeStart && start < rangeEnd) {
				out.push(makeOccurrence(event, start, end, null, start));
			}
		}
	});

	// Overrides whose master occurrence fell outside the generated window
	(event.overrides || []).forEach(o => {
		if (!o.recurrenceId) {
			return;
		}
		const t = o.recurrenceId.getTime();
		if (usedOverride.has(t) || exdates.has(t)) {
			return;
		}
		if (eventEnd(o) > rangeStart && o.start < rangeEnd) {
			out.push(makeOccurrence(event, o.start, eventEnd(o), o, o.recurrenceId));
		}
	});

	return out;
}

// Format a Date as an iCalendar value for a client request (date-only for all-day).
function icsValue(date, allDay) {
	if (!(date instanceof Date) || isNaN(date.getTime())) {
		return '';
	}
	if (allDay) {
		return date.getFullYear() + '-' + pad2(date.getMonth() + 1) + '-' + pad2(date.getDate());
	}
	return date.toISOString();
}

function repeatIcon(event) {
	return (event.recurring || event.isOverride)
		? '<span class="mc-event-repeat" title="' + esc(t('CALDAV/REPEATS', 'Repeating event')) + '">🔁</span>'
		: '';
}

/* ---------------------------------------------- custom date/time picker */

// The event form must always show DD-MM-YYYY and a 24-hour time, regardless of
// the browser locale or the SnappyMail hour-format setting. Native
// <input type="date"> / <input type="datetime-local"> follow the locale, so the
// form uses this small self-contained picker instead.

// Format a Date as DD-MM-YYYY for the visible input.
function formatDisplayDate(date) {
	return pad2(date.getDate()) + '-' + pad2(date.getMonth() + 1) + '-' + date.getFullYear();
}

// Parse a DD-MM-YYYY string (also tolerates DD/MM/YYYY, DD.MM.YYYY and
// YYYY-MM-DD) into a local midnight Date, or null when invalid.
function parseDisplayDate(text) {
	const str = ('' + (text == null ? '' : text)).trim();
	if (!str) {
		return null;
	}
	let day;
	let month;
	let year;
	let m = str.match(/^(\d{1,2})[.\-/](\d{1,2})[.\-/](\d{4})$/);
	if (m) {
		day = +m[1];
		month = +m[2];
		year = +m[3];
	} else {
		m = str.match(/^(\d{4})[.\-/](\d{1,2})[.\-/](\d{1,2})$/);
		if (!m) {
			return null;
		}
		year = +m[1];
		month = +m[2];
		day = +m[3];
	}
	if (month < 1 || month > 12 || day < 1 || day > 31) {
		return null;
	}
	const d = new Date(year, month - 1, day);
	if (d.getFullYear() !== year || d.getMonth() !== month - 1 || d.getDate() !== day) {
		return null;
	}
	return d;
}

// A custom combined date + time picker (DD-MM-YYYY, 24-hour).
function createDateTimePicker() {
	const root = document.createElement('div');
	root.className = 'mc-dtp';
	root.innerHTML =
		'<div class="mc-dtp-fields">'
		+ '<span class="mc-dtp-date-wrap">'
		+ '<input type="text" class="mc-dtp-date" inputmode="numeric" maxlength="10" autocomplete="off" spellcheck="false" placeholder="DD-MM-YYYY" aria-label="' + esc(t('CALDAV/DATE', 'Date')) + '">'
		+ '<button type="button" class="mc-dtp-cal-btn" tabindex="-1" aria-label="' + esc(t('CALDAV/PICK_DATE', 'Pick a date')) + '">📅</button>'
		+ '</span>'
		+ '<span class="mc-dtp-time-wrap">'
		+ '<select class="mc-dtp-hour" aria-label="' + esc(t('CALDAV/HOUR', 'Hour')) + '"></select>'
		+ '<span class="mc-dtp-time-sep">:</span>'
		+ '<select class="mc-dtp-minute" aria-label="' + esc(t('CALDAV/MINUTE', 'Minute')) + '"></select>'
		+ '</span>'
		+ '</div>'
		+ '<div class="mc-dtp-popup" hidden></div>';

	const dateInput = root.querySelector('.mc-dtp-date');
	const calBtn = root.querySelector('.mc-dtp-cal-btn');
	const hourSel = root.querySelector('.mc-dtp-hour');
	const minuteSel = root.querySelector('.mc-dtp-minute');
	const timeWrap = root.querySelector('.mc-dtp-time-wrap');
	const popup = root.querySelector('.mc-dtp-popup');

	// Force the 24-hour lists - no locale/hour-format involvement.
	for (let h = 0; h < 24; h++) {
		hourSel.appendChild(new Option(pad2(h), String(h)));
	}
	for (let mi = 0; mi < 60; mi++) {
		minuteSel.appendChild(new Option(pad2(mi), String(mi)));
	}

	// Local date + time kept independently of the DOM so parsing cannot drift.
	let value = nextHour();
	let viewYear = value.getFullYear();
	let viewMonth = value.getMonth();

	function readTime() {
		return {h: +hourSel.value, m: +minuteSel.value};
	}

	function syncFromValue() {
		dateInput.value = formatDisplayDate(value);
		dateInput.classList.remove('mc-dtp-invalid');
		hourSel.value = String(value.getHours());
		minuteSel.value = String(value.getMinutes());
	}

	function commitDateInput() {
		const parsed = parseDisplayDate(dateInput.value);
		if (parsed) {
			value = new Date(parsed.getFullYear(), parsed.getMonth(), parsed.getDate(), value.getHours(), value.getMinutes());
			dateInput.value = formatDisplayDate(value);
			dateInput.classList.remove('mc-dtp-invalid');
		} else {
			dateInput.classList.add('mc-dtp-invalid');
		}
	}

	dateInput.addEventListener('change', commitDateInput);
	dateInput.addEventListener('blur', commitDateInput);

	hourSel.addEventListener('change', () => {
		value = new Date(value.getFullYear(), value.getMonth(), value.getDate(), +hourSel.value, value.getMinutes());
	});
	minuteSel.addEventListener('change', () => {
		value = new Date(value.getFullYear(), value.getMonth(), value.getDate(), value.getHours(), +minuteSel.value);
	});

	function renderPopup() {
		const first = new Date(viewYear, viewMonth, 1);
		const gridStart = addDays(first, -((first.getDay() + 6) % 7));
		const today = startOfDay(new Date());
		const selected = parseDisplayDate(dateInput.value);

		let html = '<div class="mc-dtp-pop-head">'
			+ '<button type="button" class="mc-dtp-pop-nav" data-dtp-nav="-1" aria-label="' + esc(t('CALDAV/PREVIOUS', 'Previous')) + '">‹</button>'
			+ '<span class="mc-dtp-pop-title">' + esc(formatDate(first, {month: 'long', year: 'numeric'})) + '</span>'
			+ '<button type="button" class="mc-dtp-pop-nav" data-dtp-nav="1" aria-label="' + esc(t('CALDAV/NEXT', 'Next')) + '">›</button>'
			+ '</div><div class="mc-dtp-pop-grid">';
		WEEKDAYS_MON.forEach(name => {
			html += '<div class="mc-dtp-pop-dow">' + esc(name) + '</div>';
		});
		for (let i = 0; i < 42; i++) {
			const day = addDays(gridStart, i);
			const classes = ['mc-dtp-pop-day'];
			if (day.getMonth() !== viewMonth) {
				classes.push('mc-other');
			}
			if (selected && sameDay(day, selected)) {
				classes.push('mc-selected');
			} else if (sameDay(day, today)) {
				classes.push('mc-today');
			}
			html += '<button type="button" class="' + classes.join(' ') + '" data-dtp-day="' + dateInputValue(day) + '">' + day.getDate() + '</button>';
		}
		html += '</div>';
		popup.innerHTML = html;
	}

	function onDocDown(event) {
		if (!root.contains(event.target)) {
			closePopup();
		}
	}

	function closePopup() {
		popup.hidden = true;
		document.removeEventListener('mousedown', onDocDown, true);
		window.removeEventListener('resize', closePopup);
		document.removeEventListener('scroll', closePopup, true);
	}

	function openPopup() {
		const current = parseDisplayDate(dateInput.value) || value;
		viewYear = current.getFullYear();
		viewMonth = current.getMonth();
		renderPopup();
		popup.hidden = false;

		// Position with fixed coordinates so the dialog's scroll container
		// cannot clip the popup.
		const rect = dateInput.getBoundingClientRect();
		const popupWidth = 250;
		let left = rect.left;
		if (left + popupWidth > window.innerWidth - 8) {
			left = window.innerWidth - popupWidth - 8;
		}
		popup.style.left = Math.max(8, left) + 'px';
		popup.style.top = (rect.bottom + 4) + 'px';

		document.addEventListener('mousedown', onDocDown, true);
		window.addEventListener('resize', closePopup);
		document.addEventListener('scroll', closePopup, true);
	}

	calBtn.addEventListener('click', () => {
		if (popup.hidden) {
			openPopup();
		} else {
			closePopup();
		}
	});

	popup.addEventListener('click', event => {
		const nav = event.target.closest('[data-dtp-nav]');
		if (nav) {
			viewMonth += +nav.dataset.dtpNav;
			if (viewMonth < 0) {
				viewMonth = 11;
				viewYear--;
			} else if (viewMonth > 11) {
				viewMonth = 0;
				viewYear++;
			}
			renderPopup();
			return;
		}
		const dayBtn = event.target.closest('[data-dtp-day]');
		if (dayBtn) {
			const picked = parseInputDate(dayBtn.dataset.dtpDay);
			if (picked) {
				value = new Date(picked.getFullYear(), picked.getMonth(), picked.getDate(), value.getHours(), value.getMinutes());
				syncFromValue();
			}
			closePopup();
		}
	});

	syncFromValue();

	return {
		element: root,
		getValue() {
			const parsed = parseDisplayDate(dateInput.value);
			if (!parsed) {
				return null;
			}
			const time = readTime();
			return new Date(parsed.getFullYear(), parsed.getMonth(), parsed.getDate(), time.h, time.m);
		},
		setValue(date) {
			if (!(date instanceof Date) || isNaN(date.getTime())) {
				return;
			}
			value = new Date(date.getFullYear(), date.getMonth(), date.getDate(), date.getHours(), date.getMinutes());
			syncFromValue();
		},
		setTimeVisible(visible) {
			timeWrap.style.display = visible ? '' : 'none';
		},
		close() {
			closePopup();
		}
	};
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
						<label class="mc-check mc-only-this" hidden>
							<input type="checkbox" name="onlythis">
							<span class="mc-lbl-onlythis"></span>
						</label>
						<div class="mc-repeat">
							<label class="mc-field">
								<span class="mc-field-label mc-lbl-repeat"></span>
								<select name="repeat"></select>
							</label>
							<div class="mc-repeat-details" hidden>
								<div class="mc-field-row">
									<label class="mc-field mc-repeat-interval">
										<span class="mc-field-label mc-lbl-every"></span>
										<input type="number" name="repeatinterval" min="1" max="999" value="1">
									</label>
									<label class="mc-field">
										<span class="mc-field-label mc-lbl-ends"></span>
										<select name="repeatends"></select>
									</label>
								</div>
								<div class="mc-repeat-until" hidden>
									<span class="mc-field-label mc-lbl-endson"></span>
									<div class="mc-dtp-host" data-cal-dtp="until"></div>
								</div>
								<label class="mc-field mc-repeat-count" hidden>
									<span class="mc-field-label mc-lbl-after"></span>
									<input type="number" name="repeatcount" min="1" max="999" value="10">
								</label>
								<div class="mc-repeat-weekdays" hidden>
									<span class="mc-field-label mc-lbl-on"></span>
									<div class="mc-weekdays"></div>
								</div>
							</div>
						</div>
						<label class="mc-check">
							<input type="checkbox" name="allday">
							<span class="mc-lbl-allday"></span>
						</label>
						<div class="mc-field-row">
							<div class="mc-field">
								<span class="mc-field-label mc-lbl-start"></span>
								<div class="mc-dtp-host" data-cal-dtp="start"></div>
							</div>
							<div class="mc-field">
								<span class="mc-field-label mc-lbl-end"></span>
								<div class="mc-dtp-host" data-cal-dtp="end"></div>
							</div>
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
	q('.mc-lbl-repeat').textContent = t('CALDAV/REPEAT', 'Repeat');
	q('.mc-lbl-every').textContent = t('CALDAV/EVERY', 'Every');
	q('.mc-lbl-ends').textContent = t('CALDAV/REPEAT_ENDS', 'Ends');
	q('.mc-lbl-endson').textContent = t('CALDAV/REPEAT_UNTIL', 'On date');
	q('.mc-lbl-after').textContent = t('CALDAV/REPEAT_AFTER', 'After');
	q('.mc-lbl-on').textContent = t('CALDAV/REPEAT_ON', 'On');
	q('.mc-lbl-onlythis').textContent = t('CALDAV/ONLY_THIS_EVENT', 'Only this event');
	q('[data-cal-delete]').textContent = t('CALDAV/DELETE', 'Delete');
	q('[data-cal-cancel]').textContent = t('CALDAV/CANCEL', 'Cancel');
	q('[data-cal-save]').textContent = t('CALDAV/SAVE', 'Save');
	q('.mc-form-title').textContent = t('CALDAV/NEW_EVENT', 'New event');
	q('.mc-form-head .mc-close').setAttribute('aria-label', t('CALDAV/CLOSE', 'Close'));

	// Repeat frequency options
	const repeatSel = q('[name="repeat"]');
	[['', t('CALDAV/REPEAT_NONE', 'Does not repeat')],
	 ['daily', t('CALDAV/REPEAT_DAILY', 'Daily')],
	 ['weekly', t('CALDAV/REPEAT_WEEKLY', 'Weekly')],
	 ['monthly', t('CALDAV/REPEAT_MONTHLY', 'Monthly')],
	 ['yearly', t('CALDAV/REPEAT_YEARLY', 'Yearly')]].forEach(opt => {
		repeatSel.appendChild(new Option(opt[1], opt[0]));
	});
	// Ends options
	const endsSel = q('[name="repeatends"]');
	[['never', t('CALDAV/REPEAT_NEVER', 'Never')],
	 ['until', t('CALDAV/REPEAT_UNTIL', 'On date')],
	 ['count', t('CALDAV/REPEAT_AFTER', 'After')]].forEach(opt => {
		endsSel.appendChild(new Option(opt[1], opt[0]));
	});
	// Weekday checkboxes (weekly recurrence)
	const weekdayBox = q('.mc-weekdays');
	[['MO', 1], ['TU', 2], ['WE', 3], ['TH', 4], ['FR', 5], ['SA', 6], ['SU', 0]].forEach(item => {
		const label = document.createElement('label');
		label.className = 'mc-weekday';
		label.innerHTML = '<input type="checkbox" data-weekday="' + item[0] + '"><span>'
			+ esc(WEEKDAYS[item[1]]) + '</span>';
		weekdayBox.appendChild(label);
	});

	// Custom date/time pickers (fixed DD-MM-YYYY + 24-hour format)
	startPicker = createDateTimePicker();
	endPicker = createDateTimePicker();
	untilPicker = createDateTimePicker();
	untilPicker.setTimeVisible(false);
	q('[data-cal-dtp="start"]').appendChild(startPicker.element);
	q('[data-cal-dtp="end"]').appendChild(endPicker.element);
	q('[data-cal-dtp="until"]').appendChild(untilPicker.element);

	q('.mc-form-window').addEventListener('submit', saveEventForm);
	q('[name="allday"]').addEventListener('change', () => toggleFormAllDay(eventFormEls()));
	q('[name="repeat"]').addEventListener('change', () => toggleRepeatFields());
	q('[name="repeatends"]').addEventListener('change', () => toggleRepeatFields());
	q('[name="onlythis"]').addEventListener('change', () => applyOnlyThis());
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
			const chosen = state.rendered.find(item => item.domId === eventEl.dataset.calEvent);
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
	startPicker && startPicker.close();
	endPicker && endPicker.close();
	untilPicker && untilPicker.close();
	editingEvent = null;
	editingSeries = null;
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
	const series = [];
	await Promise.all(state.calendars.map(async calendar => {
		try {
			const result = await request('GetCalendarEvents', {CalendarId: calendar.id});
			const rawList = (result && result.events) || [];
			// A recurring resource yields several VEVENTs (master + overrides)
			// sharing one href; group them into a single series object.
			const byResource = new Map();
			rawList.forEach(raw => {
				const event = normalizeEvent(raw, calendar);
				if (!event) {
					return;
				}
				const key = raw.href || (event.id + '|' + calendar.id);
				if (!byResource.has(key)) {
					byResource.set(key, []);
				}
				byResource.get(key).push(event);
			});
			byResource.forEach(list => {
				const master = list.find(event => !event.recurrenceId) || list[0];
				master.overrides = list.filter(event => event.recurrenceId);
				series.push(master);
			});
		} catch (e) {
			// Ignore a single failing calendar so the others still show
		}
	}));
	state.events = series;
}

function normalizeEvent(raw, calendar) {
	const start = parseDate(raw.dtstart || raw.start);
	if (!start) {
		return null;
	}
	return {
		id: raw.uid || ('event-' + Math.random().toString(36).slice(2)),
		url: raw.href || '',
		etag: raw.etag || '',
		calendarId: calendar.id,
		title: raw.summary || t('CALDAV/UNTITLED', 'Untitled'),
		start: start,
		end: parseDate(raw.dtend || raw.end),
		allDay: !!raw.allDay,
		location: raw.location || '',
		description: raw.description || '',
		color: calendar.color || '#00639a',
		rrule: raw.rrule || '',
		rdate: raw.rdate || [],
		exdate: raw.exdate || [],
		recurrenceId: parseDate(raw.recurrenceId) || null,
		sequence: raw.sequence || '0'
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
		start: startPicker,
		end: endPicker,
		location: root.querySelector('[name="location"]'),
		description: root.querySelector('[name="description"]'),
		error: root.querySelector('.mc-form-error'),
		del: root.querySelector('[data-cal-delete]'),
		save: root.querySelector('[data-cal-save]'),
		onlythis: root.querySelector('[name="onlythis"]'),
		onlythisLabel: root.querySelector('.mc-only-this'),
		repeat: root.querySelector('[name="repeat"]'),
		repeatBox: root.querySelector('.mc-repeat'),
		repeatDetails: root.querySelector('.mc-repeat-details'),
		repeatInterval: root.querySelector('[name="repeatinterval"]'),
		repeatEnds: root.querySelector('[name="repeatends"]'),
		repeatUntil: root.querySelector('.mc-repeat-until'),
		repeatCount: root.querySelector('.mc-repeat-count'),
		repeatWeekdays: root.querySelector('.mc-repeat-weekdays'),
		weekdays: Array.from(root.querySelectorAll('[data-weekday]'))
	};
}

// Show/hide the interval/weekday/ends controls for the chosen frequency.
function toggleRepeatFields() {
	const el = eventFormEls();
	const freq = el.repeat.value;
	el.repeatDetails.hidden = !freq;
	if (!freq) {
		return;
	}
	el.repeatWeekdays.hidden = ('weekly' !== freq);
	const ends = el.repeatEnds.value;
	el.repeatUntil.hidden = ('until' !== ends);
	el.repeatCount.hidden = ('count' !== ends);
	// Keep the weekly default in sync with the event start day
	if ('weekly' === freq && !el.weekdays.some(cb => cb.checked)) {
		const start = startPicker && startPicker.getValue();
		const dow = ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'][start ? start.getDay() : new Date().getDay()];
		const cb = el.weekdays.find(item => item.dataset.weekday === dow);
		if (cb) {
			cb.checked = true;
		}
	}
}

function repeatFreqToCode(freq) {
	return ({daily: 'DAILY', weekly: 'WEEKLY', monthly: 'MONTHLY', yearly: 'YEARLY'})[freq] || '';
}

// Build an RRULE value from the repeat controls ('' when not repeating).
function readRepeatForm(el, allDay) {
	const freq = el.repeat.value;
	if (!freq) {
		return '';
	}
	const parts = ['FREQ=' + repeatFreqToCode(freq)];
	const interval = parseInt(el.repeatInterval.value, 10);
	if (interval > 1) {
		parts.push('INTERVAL=' + interval);
	}
	if ('weekly' === freq) {
		const days = el.weekdays.filter(cb => cb.checked).map(cb => cb.dataset.weekday);
		if (days.length) {
			parts.push('BYDAY=' + days.join(','));
		}
	}
	const ends = el.repeatEnds.value;
	if ('count' === ends) {
		const count = parseInt(el.repeatCount.value, 10);
		if (count > 0) {
			parts.push('COUNT=' + count);
		}
	} else if ('until' === ends) {
		const until = untilPicker.getValue();
		if (until) {
			if (allDay) {
				parts.push('UNTIL=' + until.getFullYear() + pad2(until.getMonth() + 1) + pad2(until.getDate()));
			} else {
				const end = new Date(until.getFullYear(), until.getMonth(), until.getDate(), 23, 59, 59);
				const p = n => (n < 10 ? '0' : '') + n;
				parts.push('UNTIL=' + end.getUTCFullYear() + p(end.getUTCMonth() + 1) + p(end.getUTCDate())
					+ 'T' + p(end.getUTCHours()) + p(end.getUTCMinutes()) + p(end.getUTCSeconds()) + 'Z');
			}
		}
	}
	return parts.join(';');
}

// Pre-fill the repeat controls from an existing RRULE.
function setRepeatForm(rrule, startDate) {
	const el = eventFormEls();
	const rule = parseRrule(rrule);
	const freq = (rule.FREQ || '').toLowerCase();
	el.repeat.value = ['daily', 'weekly', 'monthly', 'yearly'].includes(freq) ? freq : '';
	el.repeatInterval.value = Math.max(1, parseInt(rule.INTERVAL || '1', 10) || 1);
	el.weekdays.forEach(cb => { cb.checked = false; });
	if (rule.BYDAY) {
		rule.BYDAY.split(',').map(dayCodeToIndex).forEach(dow => {
			const code = ['SU', 'MO', 'TU', 'WE', 'TH', 'FR', 'SA'][dow];
			const cb = el.weekdays.find(item => item.dataset.weekday === code);
			if (cb) {
				cb.checked = true;
			}
		});
	}
	if (rule.COUNT) {
		el.repeatEnds.value = 'count';
		el.repeatCount.value = parseInt(rule.COUNT, 10) || 10;
	} else if (rule.UNTIL) {
		el.repeatEnds.value = 'until';
		untilPicker.setValue(parseIcsInstant(rule.UNTIL));
	} else {
		el.repeatEnds.value = 'never';
	}
	if (!rule.UNTIL) {
		untilPicker.setValue(startDate || new Date());
	}
	toggleRepeatFields();
}

// Toggle the "only this event" mode (single occurrence vs whole series).
function applyOnlyThis() {
	const el = eventFormEls();
	const onlyThis = el.onlythis.checked;
	el.repeatBox.hidden = onlyThis;
	el.calendar.disabled = onlyThis;
}

// Decide whether the form should offer a single-occurrence choice.
function setupOccurrenceChoice(event) {
	const el = eventFormEls();
	const series = event ? (event.series || event) : null;
	const recurring = !!(series && isRecurringEvent(series));
	el.onlythisLabel.hidden = !recurring;
	el.onlythis.checked = recurring;
	el.repeatBox.hidden = false;
	el.calendar.disabled = false;
	applyOnlyThis();
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
	// All-day events only need a date; hide the time part of the pickers.
	const allDay = el.allday.checked;
	if (el.start) {
		el.start.setTimeVisible(!allDay);
	}
	if (el.end) {
		el.end.setTimeVisible(!allDay);
	}
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
		el.start.setValue(start);
		if (event.allDay) {
			// DTEND from the server is exclusive; show an inclusive end date
			let inclusiveEnd = start;
			if (end && end > start) {
				inclusiveEnd = addDays(end, -1);
				if (inclusiveEnd < start) {
					inclusiveEnd = start;
				}
			}
			el.end.setValue(inclusiveEnd);
		} else {
			el.end.setValue(end && end > start ? end : new Date(start.getTime() + 3600000));
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
		el.start.setValue(start);
		// All-day end is inclusive in the UI, exclusive internally
		el.end.setValue(allDay ? addDays(end, -1) : end);
	}

	editingSeries = editing ? (event.series || event) : null;
	setupOccurrenceChoice(editing ? event : null);
	setRepeatForm(editingSeries ? editingSeries.rrule : '', el.start.getValue() || new Date());

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
	startPicker && startPicker.close();
	endPicker && endPicker.close();
	untilPicker && untilPicker.close();
	editingEvent = null;
	editingSeries = null;
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
		const startValue = el.start.getValue();
		if (!startValue) {
			showFormError(el, t('CALDAV/ERROR_DATE_REQUIRED', 'Please choose a start date'));
			return;
		}
		const startDate = startOfDay(startValue);
		const endValue = el.end.getValue();
		let endDate = endValue ? startOfDay(endValue) : null;
		if (!endDate || endDate < startDate) {
			endDate = startDate;
		}
		startParam = dateInputValue(startDate);
		// CalDAV DTEND is exclusive for all-day events
		endParam = dateInputValue(addDays(endDate, 1));
	} else {
		const start = el.start.getValue();
		if (!start) {
			showFormError(el, t('CALDAV/ERROR_DATE_REQUIRED', 'Please choose a start date'));
			return;
		}
		let end = el.end.getValue();
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

	const series = editingSeries || (editingEvent ? (editingEvent.series || editingEvent) : null);
	const recurringSeries = !!(series && isRecurringEvent(series));
	const onlyThis = !!(editingEvent && recurringSeries && el.onlythis.checked);

	showFormError(el, '');
	el.save.disabled = true;
	try {
		if (editingEvent) {
			if (onlyThis) {
				// Edit a single occurrence of a recurring series (RECURRENCE-ID override)
				params.EventId = series.id;
				params.EventUrl = series.url || '';
				params.Mode = 'occurrence';
				params.RecurrenceId = icsValue(editingEvent.recurrenceId || series.start, series.allDay);
				await saveEventRequest('UpdateCalendarEvent', params);
			} else {
				// Edit the whole series
				params.EventId = series.id;
				params.EventUrl = series.url || '';
				params.Mode = 'series';
				params.Rrule = readRepeatForm(el, allDay);
				params.Exdate = (series.exdate || []).join(',');
				if (calendarId === series.calendarId) {
					await saveEventRequest('UpdateCalendarEvent', params);
				} else {
					// Moved to another calendar: recreate the series in the target
					await saveEventRequest('CreateCalendarEvent', params);
					await saveEventRequest('RemoveCalendarEvent', {
						EventId: series.id,
						EventUrl: series.url || '',
						CalendarId: series.calendarId,
						Mode: 'series'
					});
				}
			}
		} else {
			params.Rrule = readRepeatForm(el, allDay);
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
	const el = eventFormEls();
	const series = editingEvent.series || editingEvent;
	const recurringSeries = isRecurringEvent(series);
	const onlyThis = recurringSeries && el.onlythis.checked;

	const confirmMsg = onlyThis
		? t('CALDAV/DELETE_OCCURRENCE_CONFIRM', 'Delete this occurrence?')
		: (recurringSeries
			? t('CALDAV/DELETE_SERIES_CONFIRM', 'Delete all occurrences of this event?')
			: t('CALDAV/DELETE_CONFIRM', 'Delete this event?'));
	if (!window.confirm(confirmMsg)) {
		return;
	}

	showFormError(el, '');
	el.del.disabled = true;
	try {
		const params = {
			EventId: series.id,
			EventUrl: series.url || '',
			CalendarId: series.calendarId,
			Mode: onlyThis ? 'occurrence' : 'series'
		};
		if (onlyThis) {
			params.RecurrenceId = icsValue(editingEvent.recurrenceId || series.start, series.allDay);
		}
		await saveEventRequest('RemoveCalendarEvent', params);
		closeEventForm();
		await refreshEvents();
	} catch (e) {
		showFormError(el, (e && e.message) || t('CALDAV/REQUEST_FAILED', 'Request failed'));
	} finally {
		el.del.disabled = false;
	}
}

/* ------------------------------------------------------------------ render */

// The date range covered by the current view (end exclusive).
function viewRange() {
	const cursor = state.cursor;
	if (state.view === 'month') {
		const first = new Date(cursor.getFullYear(), cursor.getMonth(), 1);
		const gridStart = addDays(first, -((first.getDay() + 6) % 7));
		return {start: gridStart, end: addDays(gridStart, 42)};
	}
	if (state.view === 'week') {
		const start = startOfWeek(cursor);
		return {start: start, end: addDays(start, 7)};
	}
	const start = startOfDay(cursor);
	return {start: start, end: addDays(start, 1)};
}

function visibleEvents() {
	const range = viewRange();
	const out = [];
	state.events.forEach(series => {
		if (!state.selected.has(series.calendarId)) {
			return;
		}
		expandSeries(series, range.start, range.end).forEach(occ => out.push(occ));
	});
	// Stable DOM keys for the click handlers
	out.forEach((occ, index) => {
		occ.domId = 'cal-event-' + index;
	});
	state.rendered = out;
	return out;
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
				+ esc(event.title) + repeatIcon(event) + '</div>';
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
				+ esc(event.title) + repeatIcon(event) + '</div>';
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
					+ '<span class="mc-event-title">' + esc(event.title) + repeatIcon(event) + '</span></div>';
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
