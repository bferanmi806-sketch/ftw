---
"ftw": minor
---

The box can send one anonymous energy report a day: the date and that UTC day's solar generation, grid import and export, battery charge and discharge and vehicle charge, in whole kWh. It goes through an independent Oblivious HTTP relay to FTW Cloud's gateway, so the relay never sees the report and FTW Cloud never sees the box's address. It follows the owner's existing statistics answer, skips any day with a gap in the energy record and any site a partner controls, and adds no question or setting. Settings shows the report beside the fleet report. This release names no relay yet, so it sends nothing until a later release pins one.
