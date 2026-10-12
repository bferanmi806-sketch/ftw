# Move FTW with your data

Keep settings, history, identity, credentials, drivers and stored goals when
moving to the new line. Use a full backup and a separate target copy. Keep
the source install and an off-host backup until the target and the return
path have passed their checks. [Svenska](#svenska).

This draft targets `v0.141.0`, which has not shipped as stable. The published
beta is `v0.141.0-beta.1`. Data-copy tests do not establish that every old
installation can switch safely. The remaining paths below need work before
the guide can ship with stable.

## Start from the installation you have

| Source | Export and import |
|---|---|
| New 0.x native or Docker | Keep the existing data. Follow the [normal update](native-beta.md), including a verified full backup. |
| Old 0.x, 1.x, 2.x or SQLite-based 3.x | Export a full `.ftwbak`, or make a stopped copy of the entire data directory. Restore that backup on the separate target before its first configured start. |
| A DuckDB beta | Preserve the DuckDB file, hot SQLite, cold archives and settings together. Run [the exact target's offline converter](history-conversion.md) on the copied data before Core opens them. |
| Home Assistant app | Download a backup that includes FTW and keep its key. Use the [offline HA exporter](https://github.com/srcfl/ftw/releases/tag/ha-export-v1-beta.1) to check and copy its FTW component, then make a full FTW backup at the old `/data` root. The complete Supervisor route is still being qualified. |
| Only a backup remains | Identify its format and source version from its metadata. Use the checked full-backup route, or the matching old image on a separate recovery copy. |
| Custom paths, external files or another host platform | Inventory the actual mounts, config seed, databases, archives, keys, drivers and start rules first. Resolve external paths before activation. |

Record the actual source version, data path, config path and startup owner.
The old ready-made Pi image uses Docker. A source running a 32-bit OS can
export a stopped copy to a 64-bit Linux target; it cannot run the new binary.
These source/platform combinations still need their own installation tests.

Active SQLite settings take precedence over an old YAML seed. The config API
masks secrets and is not a full export. Include environment files and service
or Compose definitions needed to explain the source's paths and credentials.
Preserve shared MQTT and other services used by the equipment.

## Export a complete source copy

Use the source version's full-backup UI or helper when it has one. Verify the
finished archive and copy it off the source host. A verified server copy does
not establish that the downloaded file has the same bytes.

For a source without a helper, stop Core and preserve its whole data tree,
including SQLite WAL files, keys, drivers and required archives. On a
separate 64-bit Linux copy, the target's `ftw-backup create` can open legacy
SQLite read-only without upgrading it. Pass `-core-version` with the verified
**source** version and `-config` if the config seed has another name. Keep
external files and their path mapping explicit; the helper requires its
config seed inside the data tree.

Do not start an old Core merely to inspect the migration copy. Preserve the
original bytes and use the full-backup inspector or converter. When a format
or source is unknown, keep the copy and resolve that case before starting it.

For a Home Assistant backup, follow the exporter's included README. It needs
Linux and Python 3.11 or later, checks plain and protected v2/v3 components,
and keeps checked internal driver links. It refuses outside links and unknown
layouts. The extracted copy must be writable when the FTW helper opens old
SQLite; WAL/SHM sidecars may be needed. Keep the original backup unchanged.
Use the printed source version in `ftw-backup create -core-version`, and mount
the extracted data at `/data` when making the full backup. That preserves the
source root for verified relocation; a host path is not a replacement for it.

## Restore a full backup on a new native target

Prepare a new Linux host or Pi card with [the exact release package](native-beta.md#install).
The installer starts an empty setup service. Stop and disable it before
import, so it cannot start configured Core during an unfinished move.

Copy the verified full backup to `/var/tmp/ftw-move.ftwbak`. Use the exact tag
installed on this target in place of `v0.X.Y`. The following block verifies,
restores and fixes ownership. It leaves Core stopped:

```bash
(
  set -eu
  tag=v0.X.Y
  archive=/var/tmp/ftw-move.ftwbak
  helper=/opt/ftw/releases/$tag/ftw-backup
  sudo test -x "$helper"
  sudo systemctl disable --now ftw
  sudo "$helper" verify -archive "$archive"
  sudo "$helper" restore -archive "$archive" -data /var/lib/ftw -yes
  sudo chown -R ftw:ftw /var/lib/ftw
)
```

Restore checks the archive before replacing data and prints the retained
previous-data directory. Keep that path. It maps archive-internal config
paths to `/var/lib/ftw`, while keeping other stored settings. A directory move
with unresolved external paths stops before activation. Do not remove the
path check to make a restore pass.

For DuckDB, complete the offline conversion now, using the matching converter
package and all copied sources. Keep Core stopped until it succeeds. A file
that changed during conversion stops resume; keep the original and follow
the converter's diagnosis.

For a new Docker target, use its separate Compose project and the
[verified restore recipe](native-beta.md#update-new-docker). Keep Core
stopped through any conversion. Do not attach new Core to the source's live
data volume.

## If import or return stops

Keep both Core processes and their autostart off. Retain the full backup and
all active, staging and safety directories. Do not delete a hidden directory
or bypass a check to make the command succeed.

The new helper checks a restore point's complete file list before reverting.
It refuses a partial point, an older point without that record, or a retry
that would treat a partly moved active directory as complete. Recover the
verified full backup into a separate directory instead; retain the current
files too, including readings written after that backup.

If recovery returns `safety_complete: false`, the previous complete data have
returned, but the retained rejected target is partial. Keep it for inspection;
do not start Core from it or try to switch back to it. Check the recovered
installation and its start rules before enabling control.

## Switch control and verify

1. Stop old Core and its updater. Disable the confirmed old systemd, Compose,
   timer or custom startup path. In Home Assistant, also disable **Start on
   boot** and **Watchdog**. Keep needed MQTT running.
2. Confirm that the old process has stopped. A port error or one visible
   dashboard does not establish that only one Core runs.
3. Start the prepared target. On native, run
   `sudo systemctl enable --now ftw`. Use the saved API port; migration keeps
   a custom port too.
4. Check the actual running version and binary, saved settings and credentials,
   key identity, goals, representative historical periods, archive exports,
   fresh device readings and new history writes. Check the plan and actual
   equipment feedback before accepting control.
5. At a safe time, reboot the target and check again that old Core stays off,
   exactly one Core runs, the imported data remain and new writes continue.

If a check fails, stop new Core, preserve its data and diagnose. Return with
the matching old version and its saved data, with new Core stopped. Restore
only the source start rules recorded before the move. Keep data collected on
the target; returning does not merge those readings into old Core.

## What the copy tests cover

The published `v0.140.11-beta.1` packages have passed representative SQLite/config copies
from 2.3.2 and 3.8. The published `v0.141.0-beta.1` Core and converter pair
passed DuckDB copies from 3.2.0-beta.1. The 3.4.2-beta.4
DuckDB proof used `.10` and still needs current-target qualification.
The tests check settings, credentials, goals, identity, historical data,
two starts, new writes, backup/restore and matching old return. DuckDB tests
include retained long-format Parquet and resumable conversion. Earlier
0.130.4 and 1.15.0 copies were tested with `.8`; they still need current-target
qualification.

Supervisor-format tests cover plain, compressed and encrypted version 2/3
components using the published 3.8 HA image. The offline exporter and `v0.141.0-beta.1`
copy tests also cover synthetic managed-driver choices and internal links.
They do not cover a complete Supervisor job, every community app, a signed
driver-channel move or HA entity continuity. An isolated Linux systemd test does not establish a
physical Pi cutover or a power-loss recovery.

The published `v0.141.0-beta.1` Linux ARM64 backup helper also passed process
crash tests during preservation, activation, return and a later extraction.
The tests check that incomplete copies cannot replace active data, all old
and new files remain, and a complete backup can restore on a separate target.
These tests use synthetic files and do not prove recovery from physical
power loss or an interrupted installation change.

Large archives on Pi/SD, external layouts, cross-platform and 32-bit sources,
complete Supervisor control, interrupted installation changes and physical cutover
remain release requirements. Unknown formats keep their source data and
require a checked import path.

## Svenska

Ta med dina inställningar och data. Börja med en full backup av gamla FTW
och behåll den gamla installationen. Kontrollera filen efter kopiering till
en annan disk eller dator. Använd en separat värd eller ett nytt Pi-kort som mål.

1. Identifiera gamla versionen, dataformatet och de faktiska filvägarna.
   Aktiva SQLite-inställningar gäller före gammal YAML. Maskerad API-config
   saknar hemligheter och räcker inte som backup.
2. Exportera allt som behövs: data, config, lösenord, nycklar, mål,
   drivrutiner och arkiv. Home Assistant-backup behöver sin nyckel. Använd
   den länkade exportören för FTW-komponenten och följ dess README för full
   backup med den gamla roten `/data`.
3. Installera exakt målversion på den separata Linuxmaskinen. Stoppa och
   stäng av nya tjänstens autostart före import. Använd blocket ovan för en
   verifierad `.ftwbak`; det lämnar Core stoppad.
4. Har källan DuckDB, kör målversionens konverterare på kopian innan Core
   startar. Vid ett fel: behåll båda kopiorna och lös felet.
5. Stoppa gamla Core och updater samt deras autostart. Stäng också av Home
   Assistants **Start on boot** och **Watchdog**. Behåll nödvändig MQTT.
6. Starta nya Core. Kontrollera config, mål, identitet, gamla tidsperioder,
   arkiv, färska enhetsvärden, plan och nya skrivningar. Upprepa efter en
   planerad omstart och kontrollera att gamla Core fortfarande är avstängd.

Vid ett avbrott eller fel: håll båda Core och deras autostart avstängda.
Behåll backupen och alla data-, staging- och återställningskataloger. En
ofullständig eller äldre återställningspunkt utan kvitto ska inte tvingas
igång. Återställ fullbackupen till en separat katalog och behåll också data
som skrevs efter backupen. `safety_complete: false` betyder att den sparade
avvisade målkopian är ofullständig; använd den inte för en ny start.

Vid återgång: stoppa nya Core först och använd rätt gammal version med dess
sparade data. Behåll också nya data. Guidens provade datafall är avgränsade;
hela flytten från Supervisor, egna layoutar och olika värdplattformar behöver
fortfarande egna prov före stable.
