---
"ftw": patch
---

Charge now works on a one-phase charger again when the app asks for three phases. On a charger whose levels stop below 6 A on three phases, such as a 32 A one-phase charger with the levels 1.4, 2.3 and 3.7 kW, the app sent Charge now as three-phase, because FTW reported three phases for an auto charger. A three-phase order there has no level, so the car got 0 W. FTW now runs it on one phase unless the charger is set to 3p, and reports one phase for such a charger, so the app sends what it can run.
