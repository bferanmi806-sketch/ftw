---
"ftw": patch
---

Keep showing home use at night when the solar inverter goes offline after sunset. If an offline or silent solar source last reported about 0 W (within 50 W), the live view counts it as 0 W, so grid and battery still give home use instead of a blank. An inverter that drops out while producing still shows as unknown, and history and forecasts still record solar as not measured.
