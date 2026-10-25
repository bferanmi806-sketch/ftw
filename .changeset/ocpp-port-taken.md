---
"ftw": patch
---

If Core cannot open its OCPP port, because another program holds it or the TLS certificate and key do not load, the OCPP server now fails to start with that error instead of saying it started. If only the OCPP 2.0.1 port is taken, the 1.6 server still starts for the chargers that use it. The error appears in the log and in the help report, and each charger the site names shows it as its last error until it connects, so a charger that never connects has a reason beside it.
