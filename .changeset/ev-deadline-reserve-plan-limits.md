---
"ftw": patch
---

A car whose own charger takes less than the charger's three-phase levels now charges toward its goal when no plan is available. On a step table with both one-phase and three-phase levels, the reserve FTW falls back to without a plan gave such a car a one-phase level, which no three-phase order has, and the charger was ordered 0 W. An example is a 3.7 kW plug-in hybrid on a charger whose table runs 1.4-3.7 kW on one phase and 4.1-11 kW on three. The reserve now holds to the same limits as the plan: such a car is ordered the lowest three-phase level, and once FTW sees it draw on one phase, three times the level it takes.
