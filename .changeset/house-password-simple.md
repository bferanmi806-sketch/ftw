---
"ftw": minor
---

Connecting an assistant no longer needs a terminal. When the box has no house password, Settings → Access asks you to choose one right above the assistant's code, and choosing it signs that browser in. Any browser that reaches the box may set the first one, also through a Docker port or on the box itself. A new box asks for it on the setup wizard's last step. Any password will do: a short one gets a warning, not a refusal. The Home Assistant app still needs none.

The password, not the address, now proves the owner: a browser on the box itself can let an assistant in once it is signed in. Settings → System calls it the house password and explains it without developer words. If you forget it, or someone else set it first, run `ftw password reset` on the box (`docker compose exec ftw /app/ftw password reset` on Docker), then choose a new one on the box's page.
