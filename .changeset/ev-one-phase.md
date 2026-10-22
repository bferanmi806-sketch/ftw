---
"ftw": patch
---

When a charger draws on one phase although FTW ordered three, FTW now says so and plans with what the charger delivers. In 0.155.0 an Easee charger on one phase took 3.13 kW all night against a 7.6 kW plan order: about 13 of the 24 planned kWh arrived, and the plan kept promising the rest. FTW now compares the charger's power with the site meter's phases. When one phase carries all of it and the other two less than a third, for two minutes, the charger panel says "Charging on one phase" with the phase and its current. For the rest of that charging session the plan counts the rate the charger delivered, and FTW repeats the order that got that rate. Two minutes with the draw on all three phases, or unplugging, ends it. The fuse limit is unchanged: it still reads only the site meter and FTW's own orders.
