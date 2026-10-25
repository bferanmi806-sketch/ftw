---
"ftw": patch
---

When an Easee charger's own load balancing holds the car below FTW's order, the EV sheet now says "Easee limits the charger to 12 A (its load balancing). FTW asked for 15 A; the car takes 8.3 kW." instead of "Not following". It needs easee_cloud 1.4.1, which reports Easee's allocated and signalled current only while they are below the order and answer it. A lower offer whose cause the charger does not show reads "The charger offers 12 A, below the 15 A FTW asked for." A car taking far less than the limit allows is still not following, and an old charger reason is never shown as the cause.
