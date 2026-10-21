---
"ftw": patch
---

Home use is no longer marked estimated because an OCPP charger has no car. When the charger reports Available, no car is connected and no current flows, so its power counts as a measured 0 W in live Home use and in history. Each heartbeat keeps that reading current. Unavailable and Faulted still leave the charger's power unknown. The Available a charger sends for itself (connector 0) at boot or reconnect no longer reads as an unplugged car.
