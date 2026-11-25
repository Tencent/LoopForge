# Node activity UI acceptance

This evidence uses the real platform built from the animation contribution,
a dedicated local test installation, a password session and a synthetic shell
SOP. No model service, production run or organization data is involved.

## Asset cache upgrade

The literal query in `web/index.html` is not the URL sent to a browser.
`studioFileServer` calls `stampStudioIndex` on each HTML request. Every CSS and
JavaScript reference is rewritten with the first eight bytes of the asset's
SHA-256 hash. HTML is served with `Cache-Control: no-cache`; the resulting
versioned assets use `public, max-age=31536000, immutable`.

The Chromium check kept the same browser context and its HTTP cache enabled:

1. A same-origin HTTP proxy presented the prior stylesheet and its content-hash
   URL while forwarding authentication, run APIs, streaming and other assets to
   the real platform. The baseline was the stylesheet before this contribution.
2. A second normal document navigation reused that old stylesheet. Chromium's
   DevTools Protocol reported a cache hit; the proxy received only one request
   for the old asset across both navigations.
3. The proxy then forwarded the platform's current HTML unchanged. A normal
   navigation fetched the new stylesheet URL, and the existing cached stylesheet
   did not prevent either animation from appearing. No cache clear, hard refresh,
   Playwright request routing or mocked run response was used.

| Asset | Served URL | Network requests |
| --- | --- | --- |
| Previous CSS | `/observability.css?v=b5623289d4bd5745` | 1, then reused from cache |
| Animation CSS | `/observability.css?v=e10036f176cdcba1` | 1 after the upgrade |

The stylesheet hash follows its bytes and will change with later edits. The
handwritten `v=20261031metrics2` was unchanged throughout this check. Regression
tests in `internal/httpapi/studio_test.go` also verify that changing only CSS
changes the served URL without editing HTML or recreating the handler.

## Real-service visual checks

The selected node came from a real running shell SOP. Computed styles and timed
samples confirmed spinner rotation and moving text highlights in both themes.
Reduced-motion preferences disabled both animations. The inspector was also
checked at a 390px viewport, and requesting cancellation stopped its animations
through the real run update path.

The following screenshots are fresh captures of that live inspector. They are
cropped to exclude unrelated installation controls and are static images of the
animated states.

Light theme:

![Live node activity in the light theme](assets/node-activity-live-light.png)

Dark theme:

![Live node activity in the dark theme](assets/node-activity-live-dark.png)

Validation completed on 2026-11-11: the ten node-inspector tests, the stylesheet
hash/upgrade and cache-header Go tests, JavaScript syntax checks, and the live
Chromium checks described above passed. These checks do not claim approval on
behalf of a maintainer or execution of the entire deployed acceptance suite.
