# Plan: migrar el stack de ejecución a una máquina Windows 24/5 (alta disponibilidad)

Objetivo: que **una señal verde SIEMPRE se ejecute**, eliminando los modos de
fallo vistos (suspensión del laptop, Wine frágil, pérdida de conexión al terminal,
rechazo `10031` por red, terminal que no auto-arranca). Para eso: correr el stack
de producción **completo** en una **máquina Windows dedicada, prendida 24/5, con
red cableada**, con MT5 **nativo** (sin Wine) y todo auto-arrancando + capa de
resiliencia de software (reintentos, watchdog, avisos).

> **Decisiones ya tomadas — NO re-litigar:**
> - Cuenta REAL Admirals **83110711** / `AdmiralsSC-Live`, símbolo **GOLD-T**
>   (contract_size=100, vol_min/step=0.01), apalancamiento 1:1000.
> - Riesgo **38.5% Kelly FLAT** sobre el saldo en vivo, GREENS only (`|net|≥2.0`),
>   **un trade a la vez**, LIMIT con expiración 15min. Bust aceptado (consentido).
> - **VPS gratis de Admirals requiere €5.000 de equity** → no califica con ~$450.
>   Por eso: **Windows de casa AHORA** (gratis, MT5 nativo, máquina dedicada);
>   VPS pago/gratis cuando la cuenta lo justifique (gate $25k AUM de Fase 4.3).
> - La operación MANUAL de Daniel (si hay una abierta) se deja quieta; el bot la
>   respeta vía one-at-a-time.
>
> Toda ruta file:line / comando es de 2026-06-10; re-confirmar antes de ejecutar.
> El canonical, el backtest, la Fase 1 (dedup) y la lógica del auto-exec son
> sagrados: portarlos **sin cambiar comportamiento**.

---

## Arquitectura actual (origen — máquina Linux)

Todo en `/home/emy/webapps/utils/go_trading_sweepbos_xauusd` salvo el bot:

| Componente | Hoy en Linux | Detalle |
|---|---|---|
| Postgres | `127.0.0.1:5432`, db `candles` | velas 5m/4h, `backtest_runs`+pesos canónicos, `paper_trades`/`paper_runs`. DSN en `.env` |
| Detector Go | `bin/sweepbos paper-live` | flags: `-wpp-notify -live-execute -exec-endpoint=http://127.0.0.1:9099/order`; interno 5m, `backtest_runs.id=34`, `threshold-percentile=0.95`; usa `TWELVEDATA_API_KEY` (`.env`) |
| Bridge Python | `mt5bridge/server.py` bajo Wine, `:9099` | `MT5_ARM`, magic 770077, risk 0.385, LIMIT exp 15min, each-setup-once, HALT, orders.log, poller de fills→WhatsApp, auto-reconnect a MT5 |
| MT5 terminal | Wine `~/mt5wine` (GUI) | logueado a 83110711, Algo ON |
| wpp-bot | `/home/emy/webapps/go_wpp_bot/bot.exe` (Go, ELF), `:8080` | whatsmeow + SQLite `wppstore.db`; `POST /api/send`; emparejado por QR |
| systemd --user | `sweepbos-paperlive`, `sweepbos-mt5bridge`, `wpp-bot` | linger ON |

Variables de entorno que entiende el bridge (`server.py`): `MT5_ARM`, `MT5_SYMBOL`,
`MT5_RISK_PCT`, `MT5_BRIDGE_HOST`, `MT5_BRIDGE_PORT`, `MT5_POLL_SEC`,
`MT5_DEDUP_WINDOW_SEC`, `MT5_ORDERS_LOG`, `MT5_HALT_FILE`, `MT5_WPP_ENDPOINT`,
`MT5_WPP_PHONE`, `MT5_WPP_NOTIFY`, `MT5_EXPIRATION_MIN`, `MT5_MAGIC`.

## Arquitectura objetivo (Windows, todo en UNA máquina)

```
[Windows 24/5, ethernet, never-sleep, auto-login del usuario]
  Postgres (servicio Windows, :5432, db candles)
  MT5 terminal NATIVO (auto-login, Algo ON)  ←── pip MetaTrader5 (sin Wine)
  bridge  python server.py            (NSSM service, 127.0.0.1:9099, MT5_ARM=1)
  detector sweepbos.exe paper-live    (NSSM service, → 127.0.0.1:9099)
  wpp-bot  bot.exe                     (NSSM service, :8080, QR re-emparejado)
  watchdog (NSSM service, hace /health cada 60s y avisa por WhatsApp si cae)
```
Todo en localhost (sin exponer el endpoint de órdenes a la LAN) = simple y seguro.

---

## FASE 0 — Inventario y prerequisitos (NO codear todavía)

**0.1 (Linux, origen)** Documentar exacto:
- `systemctl --user cat sweepbos-paperlive sweepbos-mt5bridge wpp-bot` → guardar
  los `ExecStart` y env reales.
- DSN y `TWELVEDATA_API_KEY` de `.env` (manejar como secreto; NO commitear).
- Versión de Postgres (`psql -V`) y tamaño/filas: `SELECT count(*) FROM candles;`
  por símbolo/intervalo; `SELECT id,created_at FROM backtest_runs ORDER BY id DESC
  LIMIT 5;` (confirmar canonical id=34 vigente).
- Confirmar que `sweepbos` compila **sin CGO** (`CGO_ENABLED=0 go build .` OK) y
  que `go_wpp_bot` **usa CGO** (SQLite) → decide su build (ver 5.2).

**0.2 (Windows, destino)** Confirmar:
- Windows 10/11 64-bit, ≥4 GB RAM, disco NVMe/SSD, **cable ethernet** (no WiFi).
- Cuenta de usuario con **derechos de admin**.
- Decidir método de transferencia Linux→Windows (LAN/`scp`, USB, o `git clone`).

**0.3** Decidir credenciales: password de MT5 (para auto-login) y que el QR de
WhatsApp se re-emparejará una vez en Windows (sesión SQLite nueva).

**Gate 0:** lista de versiones, flags, DSN, tamaño de DB y método de transferencia
confirmados antes de seguir.

### FASE 0 — Inventario RESUELTO (origen Linux, 2026-06-10)

**Versiones:** Go **1.25.1**, Python **3.11.2** (Windows usará 3.11.x), Postgres
**17.7** (Windows: instalar 17.x). `MetaTrader5>=5.0.45` (nativo Windows, sin pin
de numpy).

**DSN (NO está en `.env`; default en `db.go:20`, override por env `CANDLES_DSN`):**
`postgres://emy_admin:emy-1234@127.0.0.1:5432/candles?sslmode=disable`. En Windows,
crear rol `emy_admin`/`emy-1234` y db `candles` idénticos (o setear `CANDLES_DSN`).
`.env` solo tiene `TWELVEDATA_API_KEY` (llevarlo, es secreto).

**DB `candles` = 107 MB.** Tabla de velas = **`ohlc`** (cols: symbol, interval,
bar_time, open, high, low, close, volume, source, fetched_at):
| symbol | interval | filas |
|---|---|---|
| XAU/USD | 5m | 178 699 |
| XAU/USD | 15m | 55 027 |
| XAU/USD | 1h | 15 000 |
| XAU/USD | 4h | 5 000 |
| UUP | 5m | 40 000 (proxy DXY) |
Otras tablas: `backtest_runs` (36, **canonical = max id, hoy 36**; auto-refresca),
`paper_trades` (828), `paper_runs` (77), `backtest_trades`, `wfa_runs`/`wfa_trades`,
`shadow_trades`, `comex_expiry`, `paper_pending`.

**Servicios (ExecStart reales):**
- `sweepbos-paperlive` → `bin/sweepbos paper-live -wpp-notify -live-execute -exec-endpoint=http://127.0.0.1:9099/order`
- `sweepbos-mt5bridge` → env `MT5_ARM=1 MT5_SYMBOL=GOLD-T MT5_RISK_PCT=0.385` + bridge
- `wpp-bot` → `bot.exe` (sin args, :8080)

**Build:**
- `sweepbos` = **CGO-free** ✓ → `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o sweepbos.exe .` (verificado, cross-compila limpio).
- `wpp-bot` = **CGO** (`mattn/go-sqlite3 v1.14.42` + whatsmeow, go 1.25.1) → build EN
  Windows (Go+gcc) o mingw. Su sesión vive en `wppstore.db` (4.9 MB): intentar
  copiarla a Windows para evitar re-pair; si no levanta, re-emparejar QR.

**FALTA para cerrar Gate 0 (lo provee Daniel):** specs de la máquina Windows (versión,
RAM, ethernet sí/no), método de transferencia Linux→Windows, y confirmar admin.

---

## FASE 1 — Base Windows (instalación + energía)

**1.1 Instalar** (64-bit todo): MetaTrader 5 de Admirals, **Python 3.11.x**,
**PostgreSQL** (16/17), **Git** (opcional), **NSSM** (`nssm.exe`, gestor de
servicios). Go solo si se compila en Windows (ver Fase 5).

**1.2 Energía (crítico):** Panel de energía → plan **Alto rendimiento**;
*"Suspender" = Nunca* (con corriente); **deshabilitar hibernación**
(`powercfg /hibernate off`); **deshabilitar Fast Startup**; pantalla puede
apagarse (no afecta). Verificar: `powercfg /a` y `powercfg /q`.

**1.3 Auto-login de Windows** (para que tras un reboot todo vuelva solo):
`netplwiz` → desmarcar "Los usuarios deben escribir nombre y contraseña", o
Sysinternals **Autologon**. Sin esto, un reboot deja la sesión en el login y MT5
no arranca.

**1.4 Windows Update:** configurar **horas activas** amplias para que NO reinicie
solo a mitad de un trade; idealmente diferir reinicios.

**1.5 (Recomendado) UPS** para cortes de luz.

**Gate 1:** `powercfg /a` muestra suspensión deshabilitada; reboot de prueba →
Windows entra a la sesión solo (auto-login OK).

---

## FASE 2 — MT5 nativo

**2.1** Abrir MT5, **File → Login to Trade Account**: 83110711 / `AdmiralsSC-Live`
/ password, **marcar "Save password"**. Confirmar `[REAL]` y balance.

**2.2** **Tools → Options → Expert Advisors**: permitir Algo Trading; botón
**Algo Trading = ON** en la toolbar.

**2.3** Market Watch → agregar **GOLD-T**; confirmar specs (digits=2,
contract_size=100, vol_min/step=0.01).

**2.4** Auto-arranque del terminal en login: crear acceso directo de
`terminal64.exe` en `shell:startup`. Confirmar que al iniciar sesión MT5 abre y
**reconecta logueado** (gracias a "Save password").

**Gate 2:** reboot → Windows auto-login → MT5 abre solo, logueado, Algo ON,
GOLD-T visible con bid/ask vivos.

---

## FASE 3 — Postgres + datos

**3.1 (Linux)** Dump: `pg_dump -h 127.0.0.1 -U <user> -d candles -Fc -f candles.dump`
(formato custom). Transferir `candles.dump` a Windows.

**3.2 (Windows)** Crear rol y DB que **coincidan con el DSN del `.env`** (mismo
user/password/dbname/puerto 5432). `createdb candles`; `pg_restore -U <user>
-d candles candles.dump`.

**3.3** Verificar paridad: `SELECT count(*) FROM candles;` por símbolo/intervalo
== Linux; `SELECT max(id) FROM backtest_runs;` (canonical 34 presente); spot-check
de `paper_trades`. El servicio Postgres de Windows queda en **auto-start**.

**Gate 3:** conteos de filas Windows == Linux para `candles`, `backtest_runs`,
`paper_trades`.

---

## FASE 4 — Bridge Python nativo (sin Wine)

**4.1** Copiar `mt5bridge/` (server.py, execute.py, sizing.py, bridgelib.py).
`py -3.11 -m pip install MetaTrader5` (en Windows nativo **NO** hace falta el pin
de numpy 1.26.4; instalar el numpy que arrastre MetaTrader5).

**4.2** Probar **sin Wine, sin el truco de pty** (en Windows los std streams son
válidos): con MT5 abierto/logueado,
`set MT5_ARM=0 && set MT5_ORDERS_LOG=C:\sweepbos\orders.log &&
set MT5_HALT_FILE=C:\sweepbos\HALT && py -3.11 mt5bridge\server.py`
→ `GET http://127.0.0.1:9099/health` debe dar `arm:false`, REAL, equity correcto.
→ `POST /order` dry-run con un setup conocido → `order_check retcode=0 'Done'`,
0 colocaciones.

**4.3** Definir rutas Windows por env (no tocar código): `MT5_ORDERS_LOG=
C:\sweepbos\orders.log`, `MT5_HALT_FILE=C:\sweepbos\HALT`. Crear `C:\sweepbos\`.

**4.4** Correr `python mt5bridge\test_sizing.py` y `test_bridgelib.py` (verdes).

**Gate 4:** `/health` y `/order` dry-run nativos OK; pruebas Python verdes.

---

## FASE 5 — Detector Go + wpp-bot nativos

**5.1 Detector (sweepbos):** es **CGO-free** → cross-compile en Linux:
`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o sweepbos.exe .`
Copiar `sweepbos.exe` + `.env` (con DSN apuntando a `127.0.0.1:5432` y la
`TWELVEDATA_API_KEY`) a Windows. Smoke test: `sweepbos.exe paper-live -greens-since=2d`
(scan read-only, confirma DB + API + pesos).

**5.2 wpp-bot:** usa **CGO (SQLite)** → cross-compile desde Linux NO es directo.
Elegir UNA:
- (a) **Build en Windows**: instalar Go + un gcc (TDM-GCC/mingw), `go build` en la
  carpeta del bot. *(Recomendado: más simple y robusto.)*
- (b) **Cross-compile con mingw**: `CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1
  GOOS=windows GOARCH=amd64 go build -o bot.exe .` (instalar `mingw-w64` en Linux).
- (c) Cambiar el driver a `modernc.org/sqlite` (Go puro) y `CGO_ENABLED=0`
  (cambia el código del bot; más trabajo, evita gcc).
Copiar `bot.exe` a Windows; primer arranque **muestra QR → escanear con el
teléfono** (sesión `wppstore.db` nueva). Probar `POST /api/send` → llega WhatsApp.

**Gate 5:** `sweepbos.exe` scanea greens contra la DB Windows; `bot.exe` emparejado
y envía un WhatsApp de prueba (200).

---

## FASE 6 — Auto-arranque, orden y resiliencia de host

**6.1 Servicios NSSM** (cada uno: `AppDirectory`, `AppEnvironmentExtra` para sus
env, `AppStdout/AppStderr` a logs en `C:\sweepbos\logs\`, `Start=auto`, acción de
salida = **Restart**):
- `sweepbos-bridge` → `python.exe server.py` (env `MT5_ARM=1`, `MT5_SYMBOL=GOLD-T`,
  `MT5_RISK_PCT=0.385`, rutas de orders.log/HALT).
- `sweepbos-detector` → `sweepbos.exe paper-live -wpp-notify -live-execute
  -exec-endpoint=http://127.0.0.1:9099/order`.
- `sweepbos-wppbot` → `bot.exe`.
- `sweepbos-watchdog` → ver Fase 7.
(Postgres ya es servicio Windows; MT5 arranca por la carpeta Startup, NO por NSSM,
porque es GUI.)

**6.2 Orden de arranque:** Postgres (auto) → [auto-login] → MT5 (Startup) → bridge
(reintenta `ensure_mt5()` hasta que MT5 esté logueado) → detector + bot. Dar al
bridge un pequeño delay o confiar en su auto-reconnect (ya implementado).

**6.3 Validar reinicio:** reboot de la máquina → sin intervención, los 4
componentes vuelven y `/health` da `arm:true` conectado. Documentar tiempos.

**Gate 6:** tras un reboot limpio, en ≤3 min todo está arriba y armado sin tocar
nada.

---

## FASE 7 — Resiliencia de software (en el repo; sirve en cualquier host)

> Estas mejoras se codean en `mt5bridge/` y se testean en Linux antes de portar.
> Mantener los gates de ingeniería; no cambiar la lógica de sizing/dedup.

**7.1 Reintento en rechazos transitorios** (en `place_order`/server): si
`order_send` devuelve un retcode transitorio — **`10031` (sin conexión)**, `10004`
(requote), `10021` (sin precios), `10006`/`10027` según corresponda — reintentar
N veces con backoff corto (p.ej. 3× cada 3–5 s) **dentro de la ventana de la
señal** (la LIMIT expira a 15min). NO reintentar en `10018` (mercado cerrado),
`10019` (sin dinero), `10015` (precio inválido) — esos avisan y salen. Loguear
cada intento.

**7.2 Aviso de rechazo/expiración por WhatsApp** (hoy solo avisa fills): cuando una
orden real se **rechaza** definitivamente o la LIMIT **expira sin llenar**, mandar
WhatsApp (reusar `send_whatsapp`/`format_*`). Así un fallo nunca pasa silencioso
(hoy el `10031` se descubrió de casualidad).

**7.3 Watchdog/heartbeat** (servicio aparte o thread): cada 60 s hace `/health`;
si `ok:false` (MT5 desconectado), el bridge no responde, o `algo_trading:false`,
manda **una** alerta WhatsApp por cambio-de-estado (conectado→caído y viceversa).
Es la red de seguridad: enterarse al instante de que la ejecución está caída.

**7.4 (Opcional) Límite de pérdida diaria:** el bridge rechaza nuevas órdenes si el
equity cayó > X% en el día (defensa dura contra el 38.5% flat). Reusa el HALT path.

**Gate 7:** tests unit del reintento (transitorio→reintenta, terminal→sale) y del
clasificador de retcodes; simular `10031` (mock) y ver reintento+aviso.

---

## FASE 8 — Validación end-to-end en Windows

**8.1 Dry-run:** `MT5_ARM=0` en el servicio bridge. Forzar un green (POST simulado
con un setup real) → el detector/POST llega, `order_check`, 0 colocaciones,
orders.log escribe `dryrun`. Verificar dedup y HALT (`echo > C:\sweepbos\HALT`).

**8.2 — PAUSA: confirmar con Daniel antes de armar.**

**8.3 Armar:** `MT5_ARM=1`, primer green real **supervisado** → coloca UNA LIMIT
~lotes correctos en GOLD-T, expira si no llena, one-at-a-time, WhatsApp al fill.
Reconciliar contra MT5 y contra el espejo paper.

**Gate 8:** primer trade real en Windows validado (lotes/SL/TP/expiración/notify).

---

## FASE 9 — Cutover y baja del Linux (evitar doble-ejecución)

> **REGLA DE ORO: nunca dos detectores ARMADOS a la vez** (doble trade + confusión
> del one-at-a-time entre dos terminales/cuentas distintas no — es la misma cuenta,
> pero dos órdenes a la vez romperían el supuesto). Cutover atómico.

**9.1** Antes de armar Windows en producción: **desarmar Linux** —
`touch ~/mt5wine-dl/HALT` y `systemctl --user stop sweepbos-mt5bridge
sweepbos-paperlive` (o quitar `-live-execute`). Confirmar Linux NO ejecuta.

**9.2** Armar Windows (Fase 8.3). Vigilar 1–2 días.

**9.3** Linux queda como **máquina de desarrollo** (compilás `sweepbos.exe` ahí y
copiás). Opcional: dejar el detector Linux corriendo SOLO para alertas
(`-wpp-notify` sin `-live-execute`) — pero ojo, duplicaría señales en WhatsApp;
mejor apagarlo y que Windows sea la única fuente.

**Gate 9:** una sola fuente armada (Windows); Linux confirmado sin ejecutar.

---

## Honestidad sobre "garantizar siempre"
Ningún setup de casa es 100%: ISP, corte de luz (→ UPS), mantenimiento del broker,
o un `10031` puntual pueden ocurrir. El objetivo realista con este plan: **~99%+ y
enterarse al instante** (watchdog) cuando la ejecución se cae, con reintento que
recupera los fallos transitorios. El **VPS** (Admirals gratis a €5k, o pago) es el
grado-datacenter y el endgame natural cuando la cuenta lo justifique.

## Disciplina por fase
Tras tocar Go: `go build ./... && go test ./...`. Tras tocar sizing/bridge:
`python mt5bridge/test_sizing.py` + `test_bridgelib.py`. Commit directo en `main`
por fase. Re-confirmar file:line antes de editar. El canonical, el backtest, la
Fase 1 (dedup) y el auto-exec son sagrados: portar sin cambiar comportamiento.
Secuencia: 0→1→2→3→4→5→6→7→8 (**PAUSA antes de armar**)→8.3→9.
