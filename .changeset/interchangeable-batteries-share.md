---
"ftw": patch
---

Two or more batteries with the same power per kWh share the plan's total again, as they did in 2.x. In a planner mode FTW followed each battery's own share of the plan. The planner pays the same for a kWh on either battery, so it often put a whole slot on one and the next on the other: one battery worked while the other stood at 0 W, their charge levels drifted apart, and on a large load only one battery's planned rate was used and the rest came from the grid. FTW now splits the live battery target by capacity across such batteries, so they charge and discharge together and share a large load within their limits. A battery that reports it cannot charge or discharge hands its share to the others. A gap in charge level that has already built up stays until one battery reaches its limit. Batteries whose charge or discharge limit per kWh differs by more than 5 %, such as one on a smaller hybrid inverter, still follow their own planned energy. There is no new setting.
