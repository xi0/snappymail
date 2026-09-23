## ⚠️ Note

> **Important:**  
> This plugin is **self-contained** and does **not** require the SnappyMail CardDAV Plugin.  
> It manages its own configuration (Admin Panel) and stores its own per-account CalDAV sync data.



# 📅 SnappyMail CalDAV Plugin

A lightweight and modern **CalDAV integration** for [SnappyMail](https://snappymail.eu), proudly created by [**Mailbux.com**](https://mailbux.com) — the all-in-one **free business email hosting** solution.

![SnappyMail CalDAV Plugin](https://mailwish.com/wp-content/uploads/2025/04/logo240.png)
<img width="1713" height="1151" alt="Screenshot 2025-11-12 1613s53" src="https://github.com/user-attachments/assets/3ff28dd5-e07f-4b68-871e-039b93804d72" />
<img width="1712" height="1151" alt="Screenshot 2025-11-12 161339" src="https://github.com/user-attachments/assets/bb626f5d-e568-445c-bd90-b3664f9de5ab" />

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
- 📨 Fully compatible with [Mailbux.com](https://mailbux.com) accounts  

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
4. Set the **CalDAV Server URL** in the plugin settings (defaults to `https://my.mailbux.com/dav/cal`). Each account is configured automatically on login using this server URL.  

> ✅ Done! Your SnappyMail is now calendar-enabled.

---

## 💡 About Mailbux

[**Mailbux.com**](https://mailbux.com) provides **unlimited free business email hosting** — no hidden fees, no trials, just professional email at your own domain.

### 🌟 Why Choose Mailbux

| Feature | Mailbux.com | Google Workspace | Microsoft 365 |
|----------|--------------|------------------|----------------|
| Price | **Free** | $6/user/mo | $6/user/mo |
| Email Accounts | **Unlimited** | 1 per user | 1 per user |
| Domains | **Unlimited** | Limited | Limited |
| Custom Branding | ✅ | ❌ | ❌ |
| Calendar / Drive / Docs | ✅ | ✅ | ✅ |
| SMTP Relay for Apps | ✅ | ✅ | ✅ |

### Highlights
- 🌍 **Unlimited mailboxes & domains**  
- 💼 **Modern Webmail** with CalDAV, CardDAV, WebDAV support  
- 🔐 **Private & secure** (no ads, no data selling)  
- ⚙️ **Full admin control** with API & dashboard  
- 🧩 **White-label rebranding** — use your own brand name  
- 💌 **SMTP relay** for WordPress, Laravel, and apps  

Create addresses like:
you@yourdomain.com
info@yourdomain.com
support@yourdomain.com


> 💬 “Finally, a truly free and professional email solution.” — *Mailbux User*

---

## 📷 Screenshots

*(Optional: Add plugin or Mailbux calendar screenshots here)*

---

## 🌐 Learn More

👉 [**Mailbux.com**](https://mailbux.com) — Create your **free business email account** today.  
Unlimited mailboxes. Custom domains. Rebrandable. 100% free.

---

## 🧑‍💻 Author

**Developed by [Mailbux.com](https://mailbux.com)**  
📧 Support: [support@mailbux.com](mailto:support@mailbux.com)

---

© 2025 Mailbux.com — Powered by [Mailbux](https://mailbux.com)
