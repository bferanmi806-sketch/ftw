---
"ftw": patch
---

When the planning style asks for forecast caution and the plan has to use the point forecast instead, the plan now says so: the box page shows "Caution off" and the app's plan message carries `cautionOff`. It clears once caution works again. The forecast archive no longer credits the live meter's current load to Core's recovery, and a lasting caution fault no longer rewrites its failure record on every replan. With a car plugged in, a cautious plan that reaches every departure is now published after one Energyplan solve instead of two, which halves the time to a plan.
