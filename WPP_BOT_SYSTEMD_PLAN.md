# Plan: go_wpp_bot como servicio systemd (--user) — 24/5 sin terminal

**Objetivo.** Que `go_wpp_bot` corra siempre en background, arranque al boot, se
auto-reinicie si se cae, y exponga `POST http://127.0.0.1:8080/api/send` para que
`sweepbos paper-live -wpp-notify` y `sweepbos wpp-send` puedan enviar a WhatsApp
sin depender de una terminal abierta.

**Alcance.** Solo el proceso del bot (local, sin tunnel de Cloudflare). El tunnel
es opcional y va en el Apéndice A. NO toca el código del bot ni del trader.

**Decisiones ya tomadas (sin ambigüedad):**
- Servicio **de usuario** (`systemctl --user`), no de sistema — el bot corre como
  `emy`, lee `.env` y `wppstore.db` desde su propio dir.
- **Sin EnvironmentFile**: el binario ya hace `godotenv.Load()` desde su CWD, así
  que basta con `WorkingDirectory=`. Evita los problemas de comillas de systemd.
- `Restart=always`, `RestartSec=5`.
- **Linger ON** (`loginctl enable-linger emy`) para que el servicio viva sin sesión
  iniciada y arranque en boot.
- Unidad: `~/.config/systemd/user/wpp-bot.service`.

**Datos fijos del entorno:**
- Dir del bot: `/home/emy/webapps/go_wpp_bot`
- Binario: `/home/emy/webapps/go_wpp_bot/bot.exe` (se compila con `go build -o bot.exe ./cmd/bot`)
- Sesión WhatsApp persistida: `/home/emy/webapps/go_wpp_bot/wppstore.db` (ya pareada como `+573137054945`)
- Puerto: `8080`
- Trader: `/home/emy/webapps/utils/go_trading_sweepbos_xauusd/bin/sweepbos`

---

## Precondiciones a verificar (paso 0)

```bash
# 0.1 binario presente (si no, compilarlo)
ls -la /home/emy/webapps/go_wpp_bot/bot.exe \
  || (cd /home/emy/webapps/go_wpp_bot && go build -o bot.exe ./cmd/bot)

# 0.2 sesión pareada presente (si NO existe, ver "Pairing" abajo ANTES de seguir)
ls -la /home/emy/webapps/go_wpp_bot/wppstore.db

# 0.3 systemd --user disponible
systemctl --user is-system-running 2>/dev/null || echo "ojo: revisar systemd --user"
```

Si `wppstore.db` NO existe → primero parear una vez de forma interactiva:
```bash
cd /home/emy/webapps/go_wpp_bot && ./bot.exe   # escanear QR; esperar "Linked as: +573137054945"; Ctrl+C
```

---

## Paso 1 — Apagar cualquier instancia previa

Solo puede haber UN proceso del bot (bind `:8080` único + SQLite single-writer).

```bash
# matar el bot.exe standalone que dejó la sesión de Claude (o cualquier otro)
pkill -f '/home/emy/webapps/go_wpp_bot/bot.exe' 2>/dev/null || true
# si estaba corriendo start-bot.sh, pararlo también
/home/emy/webapps/go_wpp_bot/stop-bot.sh 2>/dev/null || true
# confirmar que :8080 quedó libre
ss -ltnp | grep ':8080' && echo "AÚN HAY ALGO EN :8080 — matarlo antes de seguir" || echo "8080 libre, OK"
```

---

## Paso 2 — Habilitar linger

```bash
loginctl enable-linger emy
loginctl show-user emy | grep Linger   # debe decir Linger=yes
```

---

## Paso 3 — Escribir la unidad

Crear `~/.config/systemd/user/wpp-bot.service` con EXACTAMENTE este contenido:

```ini
[Unit]
Description=go_wpp_bot — WhatsApp sender for sweepbos trading alerts
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/home/emy/webapps/go_wpp_bot
ExecStart=/home/emy/webapps/go_wpp_bot/bot.exe
Restart=always
RestartSec=5
# El binario hace godotenv.Load() desde WorkingDirectory; no hace falta EnvironmentFile.

[Install]
WantedBy=default.target
```

Comando para crearla sin editor:
```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/wpp-bot.service <<'UNIT'
[Unit]
Description=go_wpp_bot — WhatsApp sender for sweepbos trading alerts
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/home/emy/webapps/go_wpp_bot
ExecStart=/home/emy/webapps/go_wpp_bot/bot.exe
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
UNIT
```

---

## Paso 4 — Cargar, habilitar y arrancar

```bash
systemctl --user daemon-reload
systemctl --user enable --now wpp-bot
```

---

## Paso 5 — Validar (criterios de éxito, en orden)

```bash
# 5.1 activo (running)
systemctl --user status wpp-bot --no-pager | head -12

# 5.2 logs: debe aparecer el link y el API server
journalctl --user -u wpp-bot -n 40 --no-pager | grep -iE "Linked as|Admin API Server|:8080"
# esperado: "✅ Linked as: +573137054945" y "Admin API Server ... :8080"

# 5.3 puerto escuchando
ss -ltnp | grep ':8080'

# 5.4 endpoint vivo SIN enviar (body vacío => 400)
curl -s -o /dev/null -w 'probe HTTP %{http_code}\n' -X POST http://127.0.0.1:8080/api/send -d '{}'
# esperado: 400

# 5.5 envío real end-to-end
/home/emy/webapps/utils/go_trading_sweepbos_xauusd/bin/sweepbos wpp-send "systemd ok ✅"
# esperado: "✅ sent to 573137054945" y el mensaje en el chat "Tú"
```

Si los 5 pasan → listo. El bot ahora sobrevive logout y reboot.

---

## Rollback (deshacer todo)

```bash
systemctl --user disable --now wpp-bot
rm -f ~/.config/systemd/user/wpp-bot.service
systemctl --user daemon-reload
loginctl disable-linger emy   # opcional
```

---

## Operación cotidiana

```bash
systemctl --user restart wpp-bot          # tras recompilar bot.exe
systemctl --user stop wpp-bot             # parar
journalctl --user -u wpp-bot -f           # seguir logs en vivo
# recompilar + reiniciar tras cambios de código del bot:
cd /home/emy/webapps/go_wpp_bot && go build -o bot.exe ./cmd/bot && systemctl --user restart wpp-bot
```

---

## Gotchas (leer antes de implementar)

1. **Una sola instancia.** Con el servicio activo, NO corras `./start-bot.sh` ni
   `./bot.exe` a mano: chocan en `:8080` y compiten el lock de `wppstore.db`.
   `start-bot.sh` queda solo para cuando quieras el tunnel (ver Apéndice A) —
   y en ese caso, para el servicio primero (`systemctl --user stop wpp-bot`).
2. **Ruido de MAC en logs** (SessionCipher "mismatching MAC"): es del path de
   recepción, no afecta el envío. Si floodea, re-link limpio: parar servicio,
   `rm wppstore.db`, `./bot.exe` interactivo + QR, y re-arrancar el servicio.
3. **El servicio no compila el binario.** Si `bot.exe` no existe o está viejo,
   compílalo antes (paso 0.1 / operación cotidiana).
4. **network-online.target en --user** puede no estar garantizado; da igual,
   `Restart=always` recupera el arranque si la red tarda.
5. **Otras API keys** (Gemini/OpenAI en `.env`) son para las features de chat del
   bot, NO para enviar. El `/api/send` funciona aunque falten.

---

## Apéndice A — Tunnel de Cloudflare como segunda unidad (opcional)

Solo si necesitás acceso ENTRANTE/externo. Para enviar alertas locales no hace
falta.

`~/.config/systemd/user/wpp-tunnel.service`:
```ini
[Unit]
Description=cloudflared quick tunnel for go_wpp_bot
After=wpp-bot.service
Requires=wpp-bot.service

[Service]
Type=simple
ExecStart=/usr/bin/cloudflared tunnel --url http://127.0.0.1:8080
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
```
```bash
systemctl --user daemon-reload && systemctl --user enable --now wpp-tunnel
journalctl --user -u wpp-tunnel -n 30 --no-pager | grep -i trycloudflare   # URL pública
```

---

## Apéndice B — Checklist de implementación (para la nueva sesión)

- [ ] 0. Verificar `bot.exe` y `wppstore.db` (parear si falta)
- [ ] 1. Matar instancias previas; `:8080` libre
- [ ] 2. `loginctl enable-linger emy` → `Linger=yes`
- [ ] 3. Escribir `~/.config/systemd/user/wpp-bot.service`
- [ ] 4. `daemon-reload` + `enable --now wpp-bot`
- [ ] 5. Validar 5.1–5.5 (status, link en journal, :8080, probe 400, wpp-send real)
- [ ] (opc) Apéndice A si se quiere tunnel
- [ ] Confirmar que sobrevive: `systemctl --user restart wpp-bot` y re-validar 5.5
```
