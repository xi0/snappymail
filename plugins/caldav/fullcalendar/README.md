# FullCalendar (vendored)

SnappyMail sends a strict `Content-Security-Policy` header:

```
script-src 'self' 'unsafe-eval' 'nonce-…'
```

Only scripts from the same origin (plus nonce-tagged inline scripts) are
allowed. Loading FullCalendar from a CDN such as `cdn.jsdelivr.net` is therefore
blocked by the browser with:

> Loading the script '…/fullcalendar@6.1.15/index.global.min.js' violates the
> following Content Security Policy directive: "script-src 'self' …". The action
> has been blocked.

## What goes here

```
index.global.min.js   FullCalendar 6 "global" build.
                      Includes the dayGrid, timeGrid and list plugins plus the
                      stylesheet in a single file (matches the views used by
                      plugins/caldav/calendar.js).
```

The plugin registers this file with
`$this->addJs('fullcalendar/index.global.min.js')`, so it is compiled into the
plugin bundle and served from SnappyMail's own origin with the page nonce —
exactly what the CSP expects.

## (Re)install the file

From the plugin root, run:

```bash
./fetch-fullcalendar.sh 6.1.15
```

or download it manually:

```bash
curl -fSL \
  https://cdn.jsdelivr.net/npm/fullcalendar@6.1.15/index.global.min.js \
  -o index.global.min.js
```

Keep the version in sync with what `plugins/caldav/calendar.js` used before
(`fullcalendar@6.1.15`).
