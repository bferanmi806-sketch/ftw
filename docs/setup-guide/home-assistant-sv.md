# FTW på Home Assistant (0.x beta, pilot)

[English](../home-assistant-app.md)

> **Beta / pilot.** Appen **FTW (0.x beta)** är ny och testas först på några
> få anläggningar, med start på Home Assistant Green. Den får varje FTW-beta.
> Vill du ha den beprövade vägen: kör FTW på en
> [separat Linux-dator](update-sv.md) och koppla den till Home Assistant med
> [MQTT](../ha-integration.md).
>
> Status: föreslagen i srcfl/home-assistant-addons#21. Tills den är mergad
> syns appen inte i butiken.

## Det här behöver du

- Home Assistant OS (till exempel Home Assistant Green), amd64 eller aarch64.
  Home Assistant Container/Core har inga appar.
- Ungefär 300 MB ledigt plus plats för historik.
- Växelriktare, batteri, elmätare eller laddbox på samma nätverk.

## 1. Stoppa annan FTW först

Bara en FTW får styra anläggningen.

- **Gamla appen FTW (beta), 3.x:** **Inställningar → Appar**, stäng av
  **Starta vid uppstart** och **Watchdog**, sedan **Stoppa**. Ta en backup
  först om du vill spara den. Inställningar och historik följer inte med.
- **FTW på en annan dator:** stoppa den där.

## 2. Lägg till app-repot

1. **Inställningar → Appar → Appbutik**.
2. Menyn (⋮) uppe till höger → **Repositories**.
3. Klistra in `https://github.com/srcfl/home-assistant-addons`, välj **Lägg till**.
4. Stäng och ladda om sidan om inget hänt.

## 3. Installera

1. Öppna **FTW (0.x beta)** i butiken.
2. Välj **Installera**. Home Assistant bygger appen på enheten från FTW:s
   releasepaket och kontrollerar kontrollsumman. På en Green tar det några
   minuter.
3. Slå på **Starta vid uppstart** och **Watchdog**, sedan **Starta**.
4. Fliken **Logg** ska visa `FTW starting` och `HTTP API ready`.

## 4. Öppna FTW och ställ in

Välj **Öppna webbgränssnittet** (eller `http://<home-assistant-adress>:8080`).
Följ installationsguiden och lägg till dina enheter ([guide](sv.md)).
Ge Home Assistant en fast adress (DHCP-reservation i routern).

## 5. Koppla FTW-appen eller webappen

Nya FTW-webappen och iOS-appen kräver nya FTW 0.x, som den här appen ger.
I FTW: **Inställningar → FTW app**, och följ [parkopplingen](../pairing.md).
Har du använt webappen mot äldre FTW: ta bort den från hemskärmen och
parkoppla igen.

## Uppdateringar, backup och felsökning

- Nya betor kommer som vanliga appuppdateringar under **Inställningar →
  Uppdateringar**. Räkna med täta uppdateringar under betan. Ta backup först.
- Home Assistants backup tar med FTW:s data. Appen stoppas kort under backup.
- Syns inte appen: kontrollera repo-URL:en och ladda om. Misslyckas
  installationen: se **Inställningar → System → Loggar → Supervisor**.
  Öppnas inte webbgränssnittet: kolla appens logg, port 8080 måste vara ledig.
- Fråga i [Discord](https://discord.gg/UK2ygPBu8N) och bifoga appens logg.
