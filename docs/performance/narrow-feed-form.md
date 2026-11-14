# Narrow feed-settings form

Issue: #213. Reproduction baseline: `e4a8c26a23a7c3a5336761984dfa404eef39c607`.
Browser used locally: Chrome for Testing 145.0.7632.6, headless on Linux,
SQLite reference fixture, mobile viewport 360 × 740 CSS pixels.

## Cause

The icon-upload fieldset retains the browser's `min-width: min-content`.
Its native file input contributes an intrinsic width even though the input
already has `max-width: 100%`. That percentage is not a definite constraint
while the fieldset calculates its intrinsic minimum.

A CSSOM probe changed one rule at a time on the real feed-settings page:

| Measurement (CSS px) | Original | `.form fieldset { min-width: 0; }` |
| --- | ---: | ---: |
| Document client width | 360 | 360 |
| Document scroll width | 366 | 360 |
| Form width | 328 | 328 |
| Icon fieldset width | 349.5 | 328 |
| File input width | 325 | 303.5 |

The other fieldsets were already 328 px wide. Changing just the file input
from intrinsic width to `width: 100%` also resolved the reproduction; the
shared fieldset fix instead removes the unwanted intrinsic minimum for all
form groups. The existing file-input maximum then fits its control inside
the fieldset. No overflow clipping is added, and no controls are hidden.

This is sensitive to native-control intrinsic sizing, which depends on the
browser and fonts. The measured input inherited `15px / 22.5px Arial,
"Helvetica Neue", Helvetica, sans-serif`. We have not isolated a Chrome-version
regression or compared native execution in Safari/Firefox. A hosted Chrome
pass before the fix did not refute this local reproduction.

## Regression coverage

`TestE2ENarrowFeedForm` uses the real handler and browser at 360, 390 and
430 px, in both looks and both colour themes. It keeps the strict document
`scrollWidth <= clientWidth` check and separately checks visible fieldsets
and controls against their form bounds, including zero-sized controls and
clipped fieldsets. The one-pixel tolerance in element-bound comparisons is
for fractional geometry only, not the document-overflow assertion.

It reaches the file control and the following HTTP-authentication control by
keyboard, and runs axe-core on the narrow page. `TestE2EDarkAndNarrow`
continues to check the other subscription/settings pages; the complete e2e
suite retains the existing keyboard, theme and accessibility checks.

The new regression fails against the original stylesheet through a Go
overlay and passes with the single-property fix. These are layout and
interaction checks, not screenshot or native-device validation.
