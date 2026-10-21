---
"ftw": patch
---

Show Home use again when one device is offline. Live Home use now comes from the site meter and every device that is read, as before 0.141. A battery, charger or daytime inverter that is offline is left out and Home use is marked estimated; the dashboard shows "estimated" and "≈" instead of a blank. After sunset at the site's map pin an offline inverter counts as 0 W. Home use is unknown only when the site meter is missing or stale, or the readings do not balance. `/api/status` adds `load_quality` (measured, estimated, unknown) and `load_reason`. History and forecast quality are unchanged.
