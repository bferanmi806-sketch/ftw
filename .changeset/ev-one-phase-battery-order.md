---
"ftw": patch
---

A one-phase charger now starts while FTW raises the battery. Since 0.147.2 a car on a one-phase charger, or on a charger set to 1p, could sit at 0 W shown as limited by the main fuse with every phase far below its limit: FTW took the battery's whole new order, which the site meter could not show yet, off the room on the car's one phase, so a three-phase battery counted three times there. In the reported case Charge now waited 42 s; on a busy 16 A site with the battery stepping up every half minute the car did not start for 9 minutes. The battery's order now counts as a three-phase battery puts it, the same current on each phase. A three-phase charger's limit does not change.
