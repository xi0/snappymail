## ⚠️ Note

> **Important:**  
> This plugin is **self-contained** and does **not** require the SnappyMail CardDAV Plugin.  
> It manages its own configuration (Admin Panel) and stores its own per-account CalDAV sync data.



# 📅 SnappyMail CalDAV Plugin

A lightweight and modern **CalDAV integration** for [SnappyMail](https://snappymail.eu), proudly created by [**Mailbux.com**](https://mailbux.com) with further modifications by [**Email Service Danmark ApS**](https://email-service.dk/)

---

## ✨ Description

The **SnappyMail CalDAV Plugin** adds full calendar synchronization to your SnappyMail webmail.  
Easily view, manage, and sync events directly from your Mailbux account or any CalDAV-compatible server.

Built for performance, privacy, and simplicity — your calendar stays perfectly synced across desktop, mobile, and web.

---

## 🚀 Features

- 📆 View and manage CalDAV calendars inside SnappyMail  
- ➕ Create, ✏️ edit and 🗑️ delete events in **any** of your calendars  
- 🔁 Full support for **recurring events** (daily / weekly / monthly / yearly, with interval, weekdays and an end date or count)  
- 🔄 Two-way synchronization with any CalDAV server  
- 🔒 Secure encrypted connections  
- ⚙️ Simple configuration in SnappyMail settings  

---

## 📝 Managing events

Open the calendar dialog with the 📅 button and:

- **Create** — click **+** in the toolbar, click an empty day in the month view (all-day event), or click an empty time slot in the week/day view (1-hour event).
- **Edit** — click an existing event to open it.
- **Delete** — open an event and press **Delete**.

The event form lets you pick any of the discovered **calendars** from the dropdown, so an event can be created in — or moved between — all of your available calendars.

### 🔁 Recurring events

Choose a frequency in the **Repeat** dropdown (Daily, Weekly, Monthly or Yearly), an interval ("every N"), the weekdays for a weekly rule, and how the series ends (**Never**, **On date** or **After N occurrences**). Recurrence is stored on the server as a standard `RRULE`, and the month/week/day views expand the series into individual occurrences.

When you open an occurrence of a recurring event, the form offers an **Only this event** checkbox:

- **Checked** — the change applies to that single occurrence only. The plugin writes a `RECURRENCE-ID` override to the event resource, and deleting adds an `EXDATE` exclusion.
- **Unchecked** — the change applies to the whole series (the `RRULE` is updated, existing exclusions are preserved).

Occurrences that were modified individually are marked in the grid with a 🔁 icon.

### 📨 Calendar invites

When a received mail contains a calendar invite (an `.ics` / `text/calendar` attachment), an invite box is shown at the top of the message with the event details and two options:

- **Add to calendar** — asks for confirmation, lets you pick one of your calendars, and stores the event on your CalDAV server.
- **Accept / Tentative / Decline** — sends a proper iTIP `METHOD:REPLY` response to the organizer from your account and, when the event was added, updates your participant status (PARTSTAT) on the stored event.

Recurring invites are handled too: the invite box shows the recurrence pattern, and an invite that updates or cancels a **single occurrence** (`RECURRENCE_ID`) is applied to that occurrence only — the master series and any other overrides are preserved. Invites with `METHOD:CANCEL` show a **Remove from calendar** button instead, which deletes the whole event or just the cancelled occurrence.

The invite `LOCATION` is decoded properly (the iCalendar `\,` / `\;` / `\n` escapes) and, when it also contains a video-conferencing link (Google Meet, Zoom, Microsoft Teams, Webex, …), that link is pulled out of the address and offered as a separate clickable **Join meeting** link. The same meeting link is shown under the **Location** field when editing the stored event.

### 🕒 Timezones

Invites and stored events that use `TZID` (e.g. `DTSTART;TZID=Europe/Berlin:20260903T183000`) are supported:

- The invite box resolves the wall-clock time in the named timezone before showing it, so it is no longer mistaken for UTC.
- When an invite is added to a calendar, `TZID` times are converted to explicit UTC, so the event does not depend on a `VTIMEZONE` component being preserved by the CalDAV server.
- Stored events that still carry a `TZID` are converted to the correct UTC instant when read back, so they appear at the right time in the calendar grid.

This behaviour can be turned off with the **Calendar invites** option in the plugin settings.

---

## 🛠️ Installation

1. Download or clone this repository.  
2. Copy the plugin folder into your SnappyMail `/plugins/` directory.  
3. Enable the plugin from the SnappyMail **Admin Panel**.  
4. Set the **CalDAV Server URL** in the plugin settings. Each account is configured automatically on login using this server URL.  

> ✅ Done! Your SnappyMail is now calendar-enabled.

---

## 🧑‍💻 Author

**Developed by [Mailbux.com](https://mailbux.com)**  
**Further modifications by [**Email Service Danmark ApS**](https://email-service.dk/)**

---

© 2025 Mailbux.com
© 2026 Email Service Danmark ApS
