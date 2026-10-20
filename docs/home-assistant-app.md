# FTW on Home Assistant (0.x beta, pilot)

> **Beta / pilot.** The **FTW (0.x beta)** app is new and is being tested on
> a few sites first, starting with Home Assistant Green. It gets every FTW
> beta. If you want the long-tested path, run FTW on a
> [separate Linux host](native-beta.md) and connect it to Home Assistant with
> the [MQTT integration](ha-integration.md).
>
> Status: proposed in srcfl/home-assistant-addons#21 — until that is merged
> the app does not appear in the store. This page describes how it will work.

[Svenska](setup-guide/home-assistant-sv.md)

## What you need

- Home Assistant OS (for example Home Assistant Green) or Supervised, amd64
  or aarch64. Home Assistant Container/Core has no apps; use a
  [separate host](native-beta.md) instead.
- About 300 MB free disk for the app, plus room for FTW's history.
- Your inverter, battery, meter or charger on the same network.

## 1. Stop any other FTW first

Only one FTW may control a site.

- **Old FTW (beta) app (3.x):** open it under **Settings → Apps**, turn
  off **Start on boot** and **Watchdog**, then **Stop**. Take a backup
  first if you want to keep it. Its settings and history do not move over.
- **FTW on another computer:** stop it there before you continue.

## 2. Add the app repository

1. **Settings → Apps → App store**.
2. Menu (⋮) top right → **Repositories**.
3. Paste `https://github.com/srcfl/home-assistant-addons` and choose **Add**.
4. Close the dialog. Reload the page if the store has not changed.

## 3. Install

1. Find **FTW (0.x beta)** in the store and open it.
2. Choose **Install**. Home Assistant builds the app on your device from
   FTW's release package and checks its checksum. On a Green this takes a few
   minutes; wait for it to finish.
3. Turn on **Start on boot** and **Watchdog**, then **Start**.
4. Open the **Log** tab. You should see `FTW starting` and `HTTP API ready`.

## 4. Open FTW and set it up

Choose **Open web UI** (or go to `http://<home-assistant-address>:8080`).
A new install opens the setup wizard. Follow it to add your devices. The
[setup guide](setup-guide/en.md) explains each step.

Give Home Assistant a fixed address (DHCP reservation in the router), so the
FTW apps and your devices keep finding it.

## 5. Connect the FTW app (iPhone) or webapp

The new FTW webapp and iOS app need new FTW 0.x — this app provides it.
In FTW's web UI choose **Settings → FTW app** and follow
[Pair with one iPhone](pairing.md). If you used the webapp with an older FTW,
remove it from the home screen and pair again.

## 6. Home Assistant entities (optional)

FTW can publish its values to Home Assistant over MQTT. See
[Home Assistant integration](ha-integration.md). Use the Mosquitto broker app
and point FTW at your Home Assistant address.

## Updates

New FTW betas arrive as normal app updates under **Settings → Updates**.
Expect frequent updates during the beta. Take a backup first if you want a way
back. `ftw update` does not apply inside the app.

## Backup and restore

FTW's data (settings, history, pairing) lives in the app. Home Assistant
backups include it; the app stops briefly while the backup is taken.
Restore by restoring the backup (or only the FTW app from it).

## Troubleshooting

| Symptom | What to do |
|---|---|
| The app is not in the store | Check the repository URL, reload the page, or **⋮ → Check for updates**. |
| Install fails | Open **Settings → System → Logs → Supervisor**. A download or checksum error means the package could not be fetched; try again later. |
| Web UI does not open | Check the app **Log**. Port 8080 must be free; another app on 8080 stops FTW. |
| FTW cannot reach a device | Check the device's IP address and that it is on the same network. |
| The FTW app/webapp says the box is too old | Make sure you installed **FTW (0.x beta)**, not the old **FTW (beta)**. |

Report app problems in the
[app issue tracker](https://github.com/srcfl/home-assistant-addons/issues)
and FTW problems [here](https://github.com/srcfl/ftw/issues), or ask in
[Discord](https://discord.gg/UK2ygPBu8N). Include the app log.
