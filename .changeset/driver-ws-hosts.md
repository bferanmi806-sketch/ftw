---
"ftw": patch
---

A driver can name the WebSocket hosts it streams from in its `DRIVER` table as `ws_hosts`, the way cloud drivers already name `http_hosts`. FTW then grants the WebSocket for those hosts alone, so a saved driver needs no `capabilities.websocket` entry; hosts the config lists are kept. Opening a WebSocket now gives up after 5 s instead of 15 s, because the driver's poll and its commands wait for it.
