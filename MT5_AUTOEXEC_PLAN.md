# Plan: auto-ejecución de greens en Admirals REAL vía MT5 bridge

Objetivo: cuando el detector (`paper-live`) emite un **green** (`|net|≥2.0`),
colocar **automáticamente** una orden real en la cuenta Admirals, sized al
**38.5% del equity en el SL**, **un trade a la vez**.

> Decisiones YA tomadas (NO re-litigar): cuenta REAL (login 83110711,
> `AdmiralsSC-Live`, ~$414, leverage 1:1000), símbolo **GOLD-T** (no XAUUSD,
> contract_size=100, vol_min/step=0.01), riesgo **38.5% Kelly FLAT**, greens
> only, one-at-a-time. Daniel hizo override informado del riesgo de bust
> (ver memoria `project_mt5bridge_admirals`, `project_realsim_2026_05_15`).
> El deber de advertir ya se cumplió; mantener solo los gates de INGENIERÍA
> (no perder por un bug; sí por la estrategia elegida).

> Toda referencia file:line es de 2026-06-09; re-confirmar con grep/Read antes
> de editar. El canonical, el backtest y la lógica de Fase 1 (dedup) son sagrados.

## Estado actual (hecho)
- Runtime Wine montado (`~/mt5wine`): Wine 8.0 win10, Python 3.11, MetaTrader5
  5.0.5735 + numpy 1.26.4, terminal MT5. Helper `mt5bridge/run_wine.sh
  {terminal|connect|execute|install}`.
- `mt5bridge/connect.py` (read-only) — conexión REAL verificada.
- `mt5bridge/sizing.py` (puro, testeado `test_sizing.py`) + `mt5bridge/execute.py`
  (LIMIT pendiente, dry-run por defecto, `--arm`, one-at-a-time vs broker).
  Dry-run validado: green → 0.09 lotes, order_check 'Done'.
- **Algo Trading: ACTIVADO** en la toolbar de MT5 (Daniel, 2026-06-09).
- Suspend del laptop deshabilitado (servicios sobreviven tapa cerrada).

## Fase 1 — Refactor del core de ejecución (reutilizable)
1. Extraer de `mt5bridge/execute.py` una función pura-de-efectos
   `place_order(side, entry, sl, tp, risk_pct, arm: bool) -> dict` que: inicializa
   MT5, valida cuenta/símbolo, chequea one-at-a-time vs broker
   (`positions_get`+`orders_get`), computa lotes (`sizing.compute_lots`), arma el
   request, corre `order_check`; si `arm` y Algo-Trading ON → `order_send`.
   Devuelve `{ok, retcode, lots, actual_risk, ticket, reason}`. El CLI
   (`__main__`) la llama; el server (Fase 2) también.
2. **Decisión de lifecycle de la orden (resolver e implementar):** una LIMIT GTC
   puede llenarse horas después a un nivel viejo (el backtest entra en la vela de
   la señal). Implementar **expiración**: `type_time=ORDER_TIME_SPECIFIED` con
   `expiration = now + N velas` (recomendado N=3 en 5m = 15min) → si no se llena
   en la ventana, MT5 la cancela. Esto alinea la ejecución real con el supuesto
   de entrada del backtest.
3. `python3 test_sizing.py` sigue verde (no romper el sizing).

## Fase 2 — Bridge HTTP server (Python bajo wine)
1. `mt5bridge/server.py`: servidor HTTP (stdlib `http.server`, sin deps nuevas)
   que corre bajo wine python, bindea `127.0.0.1:9091`. Endpoints:
   - `POST /order` body `{side, entry, sl, tp, strategy, key}` → llama
     `place_order(...)`. Modo armado por env `MT5_ARM` (0=dry-run/order_check,
     1=real). Devuelve JSON del resultado.
   - `GET /health` → `{kind:REAL|DEMO, equity, positions, orders, algo_trading}`.
   - **each-setup-once para órdenes reales:** set en memoria de `key`
     (`Strategy|Side|Entry|SL|TP`) ya enviados; rechaza duplicados (defensa extra
     al one-at-a-time del broker).
   - Toda colocación se loguea (archivo `~/mt5wine-dl/orders.log` o tabla) para
     reconciliación.
2. Patrón idéntico a `go_wpp_bot` (HTTP-only, localhost, desacoplado). Wine bindea
   sockets del host → alcanzable desde Go nativo en `127.0.0.1:9091`.
3. Probar con `curl` en modo dry-run (`MT5_ARM=0`):
   `curl -s -XPOST 127.0.0.1:9091/order -d '{"side":"SELL","entry":4319.80,"sl":4336.18,"tp":4288.55,"strategy":"SilverBullet","key":"..."}'`
   → debe devolver sizing + order_check sin colocar nada.

## Fase 3 — Hook en el detector Go (`internal/paper/live.go`)
1. `LiveConfig`: agregar `LiveExecute bool`, `ExecEndpoint string`
   (`http://127.0.0.1:9091/order`), `ExecRiskPct float64` (default 0.385).
2. En `RunLive`, mantener un set `executed map[string]bool` (espejo de `alerted`).
3. En la rama del **green decoupled** (donde hoy dispara el WhatsApp:
   `if absNet >= 2.0 && cfg.Notifier != nil && !alerted[key]`), agregar en
   paralelo: `if cfg.LiveExecute && absNet >= 2.0 && !executed[key] { executed[key]=true; go postOrder(endpoint, side, entry, sl, tp, strategy, key) }`.
   **Fire-and-forget**, errores logueados nunca fatales (misma disciplina que el
   notify). El one-at-a-time real lo arbitra el bridge/broker, NO el gate de paper
   (un trade MED de paper no debe bloquear un green real; son tracks separados).
4. CLI `runPaperLive` (`cli.go`): flags `-live-execute` (default OFF),
   `-exec-endpoint`, `-exec-risk` (default 0.385). Banner cuando ON.
5. `go build ./... && go test ./...` verde. Commit. Deploy:
   `go build -o bin/sweepbos . && systemctl --user restart sweepbos-paperlive`.

## Fase 4 — Validación end-to-end en DRY-RUN (sin dinero)
1. Lanzar terminal MT5 (logueado) + `server.py` con `MT5_ARM=0` bajo wine.
2. Correr `paper-live -wpp-notify -live-execute` (bridge en dry-run).
3. Esperar un green en vivo (mercado abierto) **o** simular POSTeando un green
   conocido. Confirmar: Go POSTea, el bridge corre order_check, **no coloca nada**,
   loguea. Verificar one-at-a-time + each-setup-once (greens repetidos del mismo
   setup → 1 sola colocación intentada).

## Fase 5 — Armar + primer trade real (supervisado)
1. `MT5_ARM=1` en el server. Mantener `paper-live -live-execute`.
2. En el primer green: confirmar que coloca **una** LIMIT ~0.09 lotes con SL/TP en
   GOLD-T, que expira en N velas si no llena, y que el one-at-a-time bloquea
   greens subsiguientes mientras haya posición/orden abierta.
3. Reconciliar contra MT5 (posición, lotes, SL/TP) y contra el paper_trade espejo.

## Fase 6 — 24/5 (systemd, patrón wpp-bot)
1. Servicio para mantener el **terminal MT5 corriendo+logueado** bajo wine 24/5
   (`sweepbos-mt5term.service` → `wine terminal64.exe`; depende de DISPLAY/Xvfb si
   se corre headless — resolver: usar el :0 existente o un Xvfb dedicado).
2. Servicio `sweepbos-mt5bridge.service` → `wine python server.py`, `After`/`Wants`
   del terminal. `Restart=always`. Linger ya está ON.
3. Orden de arranque: Postgres → terminal MT5 (logueado) → bridge → paper-live.

## Fase 7 — Seguridad / reconciliación / kill-switch
1. **Kill-switch** inmediato: `-live-execute=false` + restart, y/o un archivo
   flag (`~/mt5wine-dl/HALT`) que el bridge chequea y rechaza órdenes si existe.
2. **Reconciliación de arranque:** el bridge al iniciar lee posiciones/órdenes
   reales y siembra el estado one-at-a-time/each-setup-once.
3. **Auditoría:** log de cada orden real (request + retcode + ticket) y alerta
   WhatsApp en fill/rechazo de orden real (reusar `notify`).
4. **Límite de pérdida diaria** opcional: el bridge rechaza nuevas órdenes si el
   equity cayó > X% en el día (defensa dura contra el bust de 38.5% flat).

## Track paralelo — "investigación exhaustiva de varias cosas"
Daniel mencionó varios temas de investigación aún SIN enumerar. Al iniciar la
sesión nueva: **pedirle la lista explícita** y priorizarlos; o ejecutar primero
Fases 1–5 (auto-exec) y luego la investigación. No asumir el contenido.

## Secuencia sin ambigüedad
1→2→3→4 (dry-run, sin dinero) → **pausa de confirmación con Daniel** → 5 (armar,
primer trade real supervisado) → 6 (24/5) → 7 (endurecer). Cada fase:
`go build ./... && go test ./...` tras tocar Go; commit directo en `main` por fase;
re-confirmar file:line antes de editar.
