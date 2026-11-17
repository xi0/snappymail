# Go backend

This is the Go backend intende to replace the PHP backend.

## Goal

The Go backend should serve the same endpoints with identical responses as the traditional PHP backend.
It should also serve static assets, so that the entire application is available through this backend.
Responses do not need to be byte-identical if the semantics of the responses are identical.
Files not consumed by the frontend should not be available. External dependencies should be pulled in as
needed. Plugins are enabled at build time and for now, only the "caldav" plugin should be available and
enabled for the Go backend.

## Code organization

## External dependencies

## Implementation steps

