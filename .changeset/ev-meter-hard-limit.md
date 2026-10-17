---
"ftw": patch
---

The EV's hard limit now follows the site meter. A charge is cut by the overage the meter measures on the worst phase or in total import, holds while the site is inside its limits, and rises one step at a time once the car has taken the last one. A late or lone 0 W reading from the charger can no longer stop a charge, and an import ceiling no longer makes the charger start and stop every half minute.
