# Remotly

**Selbst gehosteter Fernzugriff mit minimaler Latenz – gebaut für Büros, in denen
die Arbeitsplatzrechner im Serverrack stehen und die Mitarbeiter mit Thin Clients
arbeiten.** Funktioniert genauso von Windows auf macOS, macOS auf Windows, Linux …
– kurz: wie Parsec, aber komplett im eigenen Netz.

- **Ein Linux-Server** als Zentrale: Anmeldung, Rechteverwaltung, Vermittlung,
  Relay, Weboberfläche, Agent-Verteilung.
- **Ein Agent** auf jedem Rechner, der ferngesteuert werden soll (Windows, macOS,
  Linux).
- **Kein Client nötig:** Thin Clients und alle anderen Geräte nutzen einfach den
  Browser (Chrome, Edge, Chromium, Firefox, Safari) – mit Hardware-Dekodierung.

```
 Thin Client / Laptop (Browser)                Rack-PC (Agent)
 ┌──────────────────────────┐                  ┌──────────────────────────┐
 │ Video/Ton  ◄─────────────┼── WebRTC direkt ─┼── Bildschirm → GPU-Encoder│
 │ Maus/Tastatur ───────────┼──── im LAN ─────►┼── Eingabe-Injektion       │
 └───────────┬──────────────┘   (Fallback:     └───────────┬──────────────┘
             │                   TURN-Relay)               │
             │       Anmeldung, Rechte, Vermittlung        │
             └──────────────► Remotly-Server ◄─────────────┘
                              (Linux, HTTPS, TURN)
```

Der Server vermittelt nur. Bild, Ton und Eingaben laufen **direkt** zwischen
Thin Client und Rack-PC – das ist der kürzeste Weg und damit die niedrigste
Latenz. Nur wenn kein direkter Weg möglich ist (z. B. getrennte VLANs ohne
Routing), läuft der Datenstrom über das TURN-Relay des Servers.

---

## Funktionen

**Latenz**
- Hardware-Encoder: NVIDIA NVENC, AMD AMF, Intel Quick Sync, Apple VideoToolbox
  (automatische Erkennung mit Selbsttest, Fallback auf Software).
- **Zero-Copy unter Windows:** Desktop Duplication → GPU-Encoder, das Bild verlässt
  die Grafikkarte nicht.
- Encoder-Einstellungen für „Ultra Low Latency“: keine B-Frames, kein Lookahead,
  CBR mit Ein-Frame-Puffer.
- Frames werden in dem Moment verschickt, in dem der Encoder sie fertig hat
  (FLV/IVF mit exakten Framegrenzen statt Warten auf das nächste Frame).
- **`playout-delay = 0`** (RTP-Erweiterung, wie bei Cloud-Gaming): der Browser zeigt
  jedes Bild sofort nach dem Dekodieren an – gemessener Jitter-Puffer < 3 ms.
- Video wird nicht auf den Ton synchronisiert (kein Warten auf den Audio-Puffer).
- **Lokaler Mauszeiger (Windows):** der Cursor wird im Browser gezeichnet und nimmt
  die Form des Remote-Cursors an (Textcursor, Hand, Pfeile …) → Mausbewegung
  ohne spürbare Verzögerung.
- Mausbewegungen über einen unzuverlässigen, ungeordneten Datenkanal (ein
  verlorenes Paket hält nie neuere Positionen auf), Klicks/Tasten zuverlässig.
- Live-Anzeige in der Sitzung: Netzwerk-RTT, Eingabe-RTT, Jitter-Puffer,
  Dekodierzeit, fps, Datenrate, direkt/Relay.

**Büro-Betrieb**
- Benutzerkonten mit Rollen (Admin/Benutzer) und **Gerätezuweisung**: jeder sieht
  nur seine Rechner.
- **Windows-Dienst:** funktioniert auch am **Sperrbildschirm**, am
  **Anmeldebildschirm nach einem Neustart** und bei **UAC-Abfragen**;
  **Strg+Alt+Entf** per Knopfdruck.
- **Wake-on-LAN:** ausgeschaltete Rechner per Klick einschalten.
- **Sitzung mitnehmen:** Meldet sich ein Mitarbeiter an einem anderen Thin Client
  an, wandert seine Sitzung mit. Fremde Benutzer werden abgewiesen
  („wird gerade von … verwendet“); Admins können zuschauen/übernehmen.
- **Kiosk-Modus** für Thin Clients (`/?kiosk&auto`): Anmelden → eigener Rechner
  erscheint → ein Klick → Vollbild. Verbindet automatisch neu.
- **Ton** (Windows: WASAPI-Loopback, Opus mit 10-ms-Frames).
- **Zwischenablage** (Text) in beide Richtungen.
- Volle Tastatur im Vollbild (Alt+Tab, Windows-Taste, Esc … gehen an den
  Remote-Rechner), Cmd↔Strg-Tausch für Mac↔Windows.
- Admin-Oberfläche: Benutzer, Geräte, aktive Sitzungen (beenden), Audit-Protokoll.

**Sicherheit**
- HTTPS standardmäßig, mit eigener **lokaler CA** (einmal auf den Thin Clients
  installieren) oder eurem Firmen-Zertifikat.
- Passwörter mit **bcrypt**, Session-Cookies `HttpOnly` + `Secure` + `SameSite=Strict`,
  Origin-Prüfung (CSRF/Cross-Site-WebSocket), strenge Content-Security-Policy.
- **Brute-Force-Schutz** (Sperre pro IP und Benutzer mit exponentieller Wartezeit),
  kein Verraten existierender Benutzernamen über die Antwortzeit.
- Jeder Agent hat ein **eigenes Geheimnis** (nur gehasht gespeichert), einzeln
  widerrufbar. Registrierung nur mit Registrierungsschlüssel.
- Agents **pinnen die Server-CA** – ein fremder Server kann sich nicht als eurer
  ausgeben. Installer prüfen die **SHA-256-Prüfsumme** des Agents.
- Medien sind per WebRTC immer Ende-zu-Ende verschlüsselt (DTLS-SRTP), auch über
  das Relay. TURN-Zugangsdaten sind kurzlebig (HMAC, 12 h).
- Audit-Log aller Anmeldungen, Sitzungen und Admin-Aktionen.

---

## Schnellstart

### 1. Server installieren (Linux mit systemd)

Aus dem Release-Archiv:

```sh
tar xzf remotly-linux-amd64.tar.gz
sudo ./remotly/install.sh
```

Oder direkt aus dem Quellcode (benötigt Go ≥ 1.25):

```sh
git clone https://github.com/maxhummler-oss/remotly.git
cd remotly
sudo scripts/install-server.sh
```

Der Installer legt den Dienst `remotly` an, öffnet die Ports (ufw/firewalld) und
fragt nach dem ersten Administrator. Danach: **`https://<server-ip>/`**

| Port | Zweck |
|---|---|
| 443/tcp | Weboberfläche, API, Signalisierung |
| 80/tcp | Weiterleitung auf HTTPS |
| 3478/udp | STUN/TURN |
| 50000–50999/udp | TURN-Relay (nur genutzt, wenn keine Direktverbindung möglich ist) |

### 2. Agents auf den Rechnern installieren

In der Weboberfläche: **Verwaltung → Gerät hinzufügen**. Dort stehen fertige
Einzeiler mit Registrierungsschlüssel:

- **Windows** (PowerShell *als Administrator*): installiert den Dienst, ffmpeg (über
  winget) und die Firewall-Regel.
- **macOS / Linux** (Terminal): installiert Agent + Autostart. macOS fragt einmalig
  nach den Rechten *Bildschirmaufnahme* und *Bedienungshilfen*.

### 3. Benutzer anlegen und Rechner zuweisen

**Verwaltung → + Benutzer**: Benutzer anlegen, erlaubte Geräte ankreuzen. Fertig.

### 4. Thin Clients einrichten

1. Die Remotly-CA einmalig als vertrauenswürdige Stammzertifizierungsstelle
   installieren (Download: `https://<server>/ca.crt`, im Büro am besten per
   Gruppenrichtlinie / Thin-Client-Management). Fingerabdruck steht in der
   Verwaltung.
2. Browser im Kiosk-Modus starten:

   ```sh
   chromium --kiosk --noerrdialogs --disable-session-crashed-bubble "https://<server>/?kiosk&auto"
   ```

   - `kiosk` – reduzierte Oberfläche: Anmeldung → eigene Rechner → Vollbild
   - `auto` – verbindet automatisch, wenn dem Benutzer genau ein Rechner gehört
   - `device=<Name>` – nur diesen Rechner anzeigen

Empfohlen: aktueller Chrome/Edge/Chromium (H.264-Hardware-Dekodierung). Browser
ohne H.264 bekommen automatisch VP8 (Software-Encoding auf dem Rack-PC).

---

## Checkliste für Rack-PCs

| Thema | Empfehlung |
|---|---|
| **Bildschirm** | Rechner ohne Monitor haben kein Bild! **HDMI-/DisplayPort-Dummy-Stecker** (wenige Euro) oder einen virtuellen Display-Treiber verwenden. Auflösung des Dummys = Auflösung der Thin Clients (z. B. 1920×1080). |
| **GPU** | Jede halbwegs aktuelle NVIDIA-/AMD-/Intel-GPU mit Hardware-Encoder. Ohne GPU läuft es mit Software-Encoding (mehr CPU-Last, etwas mehr Latenz). |
| **Netzwerk** | Kabel, mind. 1 Gbit/s, Thin Clients und Rack-PCs im selben Netz bzw. mit Routing zwischen den VLANs (UDP erlauben) → Direktverbindung. |
| **Wake-on-LAN** | Im BIOS/UEFI und in den Netzwerkkarten-Einstellungen aktivieren. Der Agent meldet die MAC-Adresse automatisch. |
| **Energie** | Der Installer deaktiviert Standby im Netzbetrieb; während einer Sitzung hält der Agent Rechner und Display wach. |
| **Ton** | Ohne Ausgabegerät gibt es keinen Ton → virtuelles Audiogerät (z. B. VB-CABLE) als Standardausgabe setzen. |
| **Installation** | Als Administrator → Windows-Dienst (Sperrbildschirm, Anmeldung, UAC, Strg+Alt+Entf). |

## Latenz – realistisch betrachtet

„0 ms“ ist physikalisch nicht möglich: ein Bild muss aufgenommen, kodiert,
übertragen, dekodiert und angezeigt werden. Remotly minimiert jeden Schritt:

| Schritt | typisch (1080p60, GPU, Gbit-LAN) |
|---|---|
| Aufnahme + Kodierung (Zero-Copy NVENC/AMF) | 2–6 ms |
| Netzwerk (direkt, LAN) | < 1 ms |
| Jitter-Puffer (`playout-delay = 0`) | 0–3 ms |
| Dekodierung (Hardware) | 1–4 ms |
| Warten auf den nächsten Bildaufbau (60 Hz) | 0–16 ms |
| **Summe** | **≈ 10–25 ms** |

Bei 120/144 Hz (`"fps": 120` in der Agent-Konfiguration und ein entsprechender
Dummy-Stecker) halbiert sich der letzte Posten. Im automatisierten Test in einer
virtuellen Umgebung (reine Software-Kodierung/-Dekodierung) wurden ≈ 22–30 ms
gemessen, Eingabe-Rundlaufzeit ≈ 1–3 ms. Mit lokalem Mauszeiger (Windows) ist die
Mausbewegung ohnehin verzögerungsfrei.

In der Sitzung zeigt die Leiste oben die geschätzte Latenz; 📊 öffnet Details.

## Bedienung in der Sitzung

| | |
|---|---|
| Leiste einblenden | Maus an den oberen Bildschirmrand oder **Strg+Alt+Shift+M** |
| ⛶ Vollbild | übernimmt die komplette Tastatur (Alt+Tab, Windows-Taste, Esc …). Zum Verlassen **Esc gedrückt halten**. |
| 🎯 Maus fangen | relativer Mausmodus für 3D/Spiele (Pointer-Lock) |
| ⌘⇄Strg | Cmd und Strg tauschen (automatisch an bei Mac ↔ Windows) |
| ⌃⌥⌦ | Strg+Alt+Entf |
| Zwischenablage | automatisch: beim Einfügen (Strg/Cmd+V) und beim Zurückkehren in das Fenster |

---

## Konfiguration

### Server – `/etc/remotly/server.json`

| Feld | Standard | Bedeutung |
|---|---|---|
| `name` | `Remotly` | Anzeigename (z. B. Firmenname) |
| `listen` | `:443` | HTTPS-Adresse |
| `httpRedirect` | `:80` | HTTP→HTTPS-Weiterleitung (leer = aus) |
| `tls` | `auto` | `auto` (eigene CA), `files` (`certFile`/`keyFile`, z. B. Firmen-PKI), `off` |
| `hostnames` | – | zusätzliche DNS-Namen im Zertifikat, z. B. `["remotly.firma.local"]` |
| `turnPort` | `3478` | TURN/STUN (0 = aus) |
| `relayPortMin`/`Max` | `50000`/`50999` | UDP-Ports für Relay-Verbindungen |
| `publicIp` | automatisch | IP, unter der Thin Clients den Server erreichen |
| `sessionHours` | `12` | Gültigkeit einer Anmeldung |
| `dataDir` | `/var/lib/remotly` | Benutzer/Geräte (`state.json`), Audit-Log, Zertifikate |

Befehle:

```sh
sudo remotly-server -config /etc/remotly/server.json user add NAME [--admin] [--display "Vor Nachname"]
sudo remotly-server -config /etc/remotly/server.json user passwd NAME
sudo remotly-server -config /etc/remotly/server.json user list
sudo remotly-server -config /etc/remotly/server.json enroll-key [--rotate]
```

### Agent – `agent.json`

Ort: Windows `C:\Program Files\Remotly\agent.json` (Dienst) bzw.
`%LOCALAPPDATA%\Remotly\agent.json`, macOS `~/Library/Application Support/Remotly/`,
Linux `~/.config/remotly/`.

| Feld | Standard | Bedeutung |
|---|---|---|
| `fps` | `60` | Bildrate (z. B. 120 für High-Refresh) |
| `bitrateKbps` | `30000` | Datenrate (LAN: 20–50 Mbit/s sind unproblematisch) |
| `maxWidth` | `0` | 0 = native Auflösung (ermöglicht Zero-Copy), sonst herunterskalieren |
| `encoder` | `auto` | oder fest: `h264_nvenc`, `h264_amf`, `h264_qsv`, `h264_videotoolbox`, `libx264` |
| `capture` | `auto` | Windows: `ddagrab` (Standard) / `gdigrab` |
| `display` | `0` | Monitor (0 = Hauptbildschirm) |
| `keyframeInterval` | `4` | Sekunden; bei Paketverlust wird sofort ein neues Keyframe erzeugt |
| `localCursor` | `auto` | `auto` (Windows an), `on`, `off` |
| `audio` | `auto` | `auto`, `off` (oder eigene ffmpeg-Eingabe in `audioArgs`) |
| `clipboard` | `true` | Zwischenablage synchronisieren |

`remotly-agent probe` zeigt erkannte Encoder, Aufnahme-Varianten und Ton.
Nach Änderungen: Dienst neu starten (`Restart-Service RemotlyAgent`).

---

## Fehlersuche

| Problem | Lösung |
|---|---|
| Schwarzes Bild / „capture failed“ | Kein Monitor angeschlossen → Dummy-Stecker. Log: `C:\Program Files\Remotly\agent.log` |
| Browser warnt vor Zertifikat | `ca.crt` auf dem Thin Client als Stammzertifikat installieren |
| „Relay“ statt „Direkt“ | UDP zwischen Thin-Client-Netz und Rack-Netz erlauben |
| Hohe Latenz | 📊 öffnen: Dekodierung hoch → Thin Client ohne HW-Dekodierung; RTT hoch → WLAN/Netz; Encoder `libx264` → GPU-Treiber/ffmpeg prüfen |
| Kein Ton | Windows: Standard-Ausgabegerät vorhanden? macOS: BlackHole installieren |
| Sperrbildschirm nicht steuerbar | Agent als Administrator installieren (Dienst-Modus) |
| macOS: kein Bild/keine Eingabe | Rechte *Bildschirmaufnahme* und *Bedienungshilfen* für `remotly-agent` erteilen |

## Grenzen

- Kein Mikrofon-, Webcam- oder USB-Durchreichen (für Telefonie im Büro das Headset
  am Thin Client mit einem Softphone/Teams im Browser nutzen).
- Ein Monitor pro Sitzung (wählbar über `display`).
- macOS: kein Zugriff auf das Anmeldefenster vor der Benutzeranmeldung; Cursor wird
  im Video übertragen.
- Linux-Agent: nur X11 (kein Wayland), Eingabe über `xdotool`.
- Der Viewer läuft im Browser – es gibt keinen nativen Client.

## Entwicklung

```sh
make            # Server (linux) + Agents (windows/darwin/linux, amd64/arm64)
make test       # Unit-Tests (mit Race-Detector)
make vet        # go vet für alle Zielplattformen
make package    # Release-Archive in dist/
```

Reines Go ohne cgo – alles lässt sich unter Linux für alle Plattformen bauen.
Aufnahme/Kodierung übernimmt `ffmpeg`, WebRTC kommt von [Pion](https://github.com/pion/webrtc),
macOS-Systemaufrufe laufen über [purego](https://github.com/ebitengine/purego).

```
cmd/remotly-server     Server-Programm (CLI)
cmd/remotly-agent      Agent-Programm (CLI, Windows-Dienst)
internal/server        Hub: Benutzer, Geräte, Signalisierung, TURN, TLS, API, Weboberfläche
internal/server/web    Viewer + Admin-Oberfläche (HTML/CSS/JS, ohne Build-Schritt)
internal/agent         WebRTC-Sitzungen, Codec-Wahl, Steuerkanal
internal/agent/stream  ffmpeg-Pipelines (Video/Ton), FLV/IVF/Ogg-Parser, WASAPI-Loopback
internal/agent/input   Maus/Tastatur, Cursorform, Zwischenablage, Desktop-Wechsel (Win/Mac/Linux)
internal/agent/winsvc  Windows-Dienst, Start auf dem Anmelde-Desktop, Strg+Alt+Entf
```
