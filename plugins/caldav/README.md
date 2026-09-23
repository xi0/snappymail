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

---

## 📨 Calendar invites

When a received mail contains a calendar invite (an `.ics` / `text/calendar` attachment), an invite box is shown at the top of the message with the event details and two options:

- **Add to calendar** — asks for confirmation, lets you pick one of your calendars, and stores the event on your CalDAV server.
- **Accept / Tentative / Decline** — sends a proper iTIP `METHOD:REPLY` response to the organizer from your account and, when the event was added, updates your participant status (PARTSTAT) on the stored event.

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
