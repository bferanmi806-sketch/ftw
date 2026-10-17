---
"ftw": patch
---

The import ceiling now caps the mean grid import over each local clock hour, as a power tariff bills it, instead of every second. Only the site meter counts toward the hour's budget, and a restart keeps what the hour has already imported. The EV and battery are cut only when the hour's budget needs it, so a short house peak no longer throttles the car, and a quiet start to the hour lets it charge above the ceiling later in that hour. Increases wait for a step of room, at most once a minute, five minutes after a cut and not in the hour's last quarter, so the charger does not hunt. A car starts only with room for its minimum under the ceiling itself, so it never starts on one hour's slack only to stop when the next hour begins. A meter gap never makes room. The physical fuse stays an instant limit. `/api/status` shows the hour's budget left (`peak_import_hour_left_wh`) and the import limit in force (`peak_import_limit_w`).
