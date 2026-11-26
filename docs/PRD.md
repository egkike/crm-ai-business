# PRD: CRM-AI-Business

> **Estado**: Aprobado (owner, 2026-11-26) — revisión v1.11 en curso
> **Owner**: Kike
> **Versión**: 1.11
> **Última actualización**: 2026-11-26

---

## 1. Contexto y Problema

### 1.1 Contexto

Los negocios pequeños y medianos — consultorios, talleres, salones, comercios de barrio — no
tienen recursos para contratar personal que administre la relación con sus clientes, ni
presupuesto para implementar sistemas CRM complejos y caros. El resultado es un seguimiento
manual e informal que pierde contactos, reclamos y reincidencias: el dinero que se olvida.

En paralelo, los agentes de IA conversacionales maduraron hasta operar canales de mensajería
(WhatsApp, Telegram, e-mail) de forma autónoma y confiable. El precedente inmediato es
[mcp-appointments-crm](https://github.com/egkike/mcp-appointments-crm): un servidor MCP
self-hosted en Go + SQLite, verificado en producción, donde el agente **Hermes** es la
interfaz natural del negocio y el backend expone herramientas por el protocolo
**MCP (Model Context Protocol)**. Ese proyecto demostró que eliminar la app/web para el
cliente final — delegando la conversación al agente — quita toda la fricción de UI y deja un
binario ultraligero, autocontenido y barato de mantener.

CRM-AI-Business extiende esa tesis de un dominio (reservas) al dominio completo de la
relación con el cliente: catálogo, tickets con workflows, reservas, fidelización y reportes.

### 1.2 Problema

Un negocio pequeño hoy:

- **Pierde seguimiento**: cada contacto de cliente (consulta, reclamo, pedido de asistencia)
  queda en chats dispersos que nadie administra ni recupera.
- **No tiene vista del cliente**: historia de compras, reclamos y turnos repartida entre
  teléfonos, cuadernos y memoria del dueño.
- **Olvida reactivar clientes**: el cliente que dejó de venir desaparece silenciosamente, y
  con él su valor de por vida.
- **No puede pagar** un CRM tradicional ni a una persona que lo administre.

### 1.3 Solución Propuesta

Un backend **Go + SQLite auto-hosteado en la VPS del negocio**, sin interfaces web/móvil para
clientes finales, expuesto como **Servidor MCP** (Streamable HTTP, loopback) al que
**Hermes** se conecta como vecino local. Hermes conversa con los clientes por WhatsApp y
e-mail, y con el equipo (Owner/Admin/Staff) por un único bot de Telegram; cada operación de
negocio pasa por las tools MCP con identidad y permisos por caller. Un **panel web interno**
(Astro + Tailwind + TS, embebido en el mismo binario) sirve al equipo para administrar
tickets, workflows, catálogo, reservas y reportes. El sistema genera **alertas pendientes**
que Hermes entrega por los canales; el backend nunca envía mensajes por sí mismo. Producto
**open source (MIT)** con licencia, documentación e instalación de primer nivel.

El **valor central del producto**: Hermes no atiende pasivamente — **conduce cada contacto
del Cliente hacia la resolución efectiva de su motivo**, desde el primer saludo (disposición
a resolver, descubrimiento del problema, cierre siempre con una pregunta concreta), por
los caminos que el negocio tiene: catálogo, venta directa (link o vendedor), reclamo,
asistencia o turno.

---

## 2. Objetivos y Métricas de Éxito

### 2.1 Objetivos (SMART)

- [ ] **O1**: Publicar una v1 completa y funcional (fases F1–F6) como producto open source
  MIT con instalación reproducible documentada (`install.sh` + release con SHA256) y una demo
  end-to-end reproducible: cliente escribe por WhatsApp → Hermes crea el ticket → el staff lo
  atiende.
- [ ] **O2**: Verificar cada RF Must mediante criterios Gherkin ejecutables/observables en CI,
  incluyendo un **techo de escala validado** (volumen sintético de 10.000 clientes) que el
  producto declara y prueba, no asume.
- [ ] **O3**: Lograr que el negocio completo (configuración, tickets, workflows, reservas,
  fidelización, reportes) opere sin terminal salvo dos pasos documentados: `setup
  --create-owner` y el acceso al panel (túnel SSH o receta opt-in).
- [ ] **O4**: Entregar la documentación de seguridad del canal de Hermes (allowlist mínima de
  tools por plataforma) con comandos verificados y prueba final, para que ninguna instalación
  deje al runtime con acceso full-access por default.

### 2.2 KPIs

Decisión de producto (entrevista): **sin metas de adopción**. Los KPIs miden calidad y
completitud del producto, no crecimiento de comunidad.

| Métrica | Baseline | Target | Plazo |
|---------|----------|--------|-------|
| RF Must con criterios de aceptación verificados | 0 | 100% | cierre de cada fase |
| p95 de respuesta de tools MCP (loopback, DB con 10k clientes sintéticos) | — | < 200 ms | F1 y mantenido en CI |
| Cobertura de tests Go (backend) | — | > 80% | cada fase |
| Tiempo de instalación end-to-end documentado (VPS limpia → demo) | — | < 30 min | F6 |
| Uptime del servicio (self-hosted, systemd/launchd con restart) | — | ≥ 99% mensual | operación |
| Incidentes de seguridad por config default abierta (allowlist Hermes) | — | 0 aceptados como "normal" | documentación + re-audit tras upgrade |

---

## 3. Alcance

### 3.1 In Scope

- **Núcleo**: backend Go + SQLite (WAL, FTS5) auto-hosteado, binario único, sin servicios
  externos (sin Redis, sin Node en producción, sin Docker).
- **Servidor MCP**: Streamable HTTP POST-only (spec 2025-11-25) en `127.0.0.1:3001`;
  identidad por header `X-Caller-Id` resuelta contra la base (RBAC owner/admin/staff/client);
  `tools/list` filtrado por rol del caller.
- **Canales (todos manejados por Hermes)**: clientes por **WhatsApp** (conversacional,
  inmediato) y **e-mail** (asíncrono con cadencia casi inmediata: bridge pollea la casilla
  cada N minutos — default ~5, configurable — y Hermes responde conduciendo; adjuntos out
  of scope v1); equipo (Owner/Admin/Staff) por **un único bot de Telegram** con resolución
  per-emisor (`request_contact` una sola vez → teléfono → match contra `accounts`).
- **Doctrina de atención resolutiva**: Hermes conduce cada contacto hacia su resolución desde
  el primer mensaje (principios + ejemplos en la skill, sin libretos literales); caminos:
  consulta de catálogo, venta (link directo de compra o derivación al área Ventas),
  reclamo/asistencia → ticket a su tipo y área, turno → reserva.
- **Dominio de negocio**:
  - Clientes: identidad multi-canal por canales probados (teléfono de WhatsApp y/o correo
    e-mail como identificadores naturales únicos), auto-registro en el canal por el que
    llega, herramienta de fusión de duplicados (`merge_clients`) con alerta de sugerencia.
  - Catálogo unificado: `catalog_items` con `kind` (producto|servicio); solo servicios
    (con duración) son reservables; búsqueda full-text para Hermes (`search_catalog`).
  - Tickets: cada contacto relevante genera un ticket; historial auditable de eventos.
  - **Motor de workflows configurable**: tipos de ticket, estados, transiciones y **reglas
    declarativas** (notificaciones on-enter/on-exit, deadlines con aviso overdue,
    auto-asignación round-robin por área, rol autorizado por transición). Sin scripting.
  - Reservas: agenda sobre **staff agendable** con **tres clases de cita** (servicio
    presencial, asistencia a domicilio, delivery de producto) vía `bookings.kind` +
    dirección; horarios semanales, excepciones, buffer de traslado/preparación,
    disponibilidad por staff específico **o por área**, crear/cancelar/reprogramar.
  - Fidelización: reportes (reincidencia, inactivos, ranking) + recordatorios automáticos de
    inactividad como alertas pendientes.
  - Reportes: dashboard operativo + export CSV + gráficos simples.
- **Panel web interno**: Astro + Tailwind + TS, build estático **embebido en el binario**
  (`go:embed`), servido en `127.0.0.1:3002` — login por sesión, board de tickets, editor de
  workflows, settings del negocio, reportes.
- **Notificaciones**: 100% vía Hermes — el sistema solo genera **alertas pendientes**
  (`get_pending_alerts` / `mark_alert_as_sent`); scheduler in-process (goroutine) con estado
  en SQLite.
- **Bootstrap**: CLI mínimo `crm-ai-business setup --create-owner`; todo lo demás se
  configura desde el panel.
- **Instalación**: covenant del proyecto de referencia — Linux/macOS nativo, servicio
  user-level (systemd/launchd), sin Docker, Windows vía WSL2; wizard + `install.sh` pineado
  con SHA256; uninstall con dry-run por defecto; recetas de acceso remoto al panel
  documentadas como opt-in.

### 3.2 Out of Scope

- App web o móvil para clientes finales (Hermes es la única interfaz de clientes).
- Docker/contenedores.
- SaaS multi-tenant (una instalación por negocio, self-hosted).
- Pagos, facturación, integraciones con medios de pago.
- Inventario con control de cantidades/stock.
- Puntos, niveles y campañas de fidelización; marketing masivo.
- i18n multi-idioma de UI y mensajes (v1: español; i18n como fase futura si hay demanda).
- Windows nativo (WSL2, igual que el covenant del proyecto de referencia).
- IA dentro del backend: el sistema no razona — solo datos y reglas; la IA vive en Hermes.
- API pública REST para terceros (la superficie externa es el MCP).
- Chat embebido con Hermes dentro del panel (exploración futura, condicionada a que Hermes
  exponga una API conversacional HTTP).
- Binding de identidad interna por `telegram_user_id` (evolución futura documentada; v1 usa
  `request_contact` + teléfono).
- 2FA TOTP en el login del panel (fase posterior, Could).
- Reverse proxy/VPN como producto: el binario es siempre loopback; Caddy+HTTPS y
  Tailscale/WireGuard son **recetas documentadas**, no entregables del binario.

### 3.3 Asunciones

- El equipo interno accede al panel por **túnel SSH** (por defecto) o por una receta opt-in
  (Caddy+HTTPS / Tailscale) que cada dueño elige; un staff remoto sin acceso SSH requiere que
  el dueño habilite una receta. Si esa asunción vence (staff remoto frecuente), se reabre la
  decisión de exposición.
- Cada usuario interno tiene **teléfono registrado en su cuenta** (necesario para la
  resolución de identidad por Telegram) y usuario de Telegram activo.
- Hermes está instalado como **vecino same-host** y su gateway cumple el contrato per-sender
  (`X-Caller-Id` verbatim por request, rechazo de desconocidos antes de tool call).
- El Owner sigue la **allowlist mínima de Hermes** documentada (decisión de producto: el
  auditor es documentación, no código).
- VPS mínima: 1 vCPU / 1 GB RAM (escala micro: 1–5 staff, ~1.000 clientes activos, decenas de
  tickets y reservas por día).
- Sin click-tracking de campañas en v1: la conducción conversacional parte del contexto
  real (el Cliente escribió al negocio), no de datos que el agente no tiene; los links
  `wa.me` con parámetros de campaña quedan como evolución futura documentada.
- La recepción de ubicaciones por WhatsApp depende de que el bridge de Hermes entregue el
  payload de location al agente; si no lo entrega, Hermes pide la dirección escrita
  (fallback natural, sin bloqueo). La ubicación en vivo (live location) nunca se almacena.
- **Canal e-mail**: identidad, doctrina, tickets y alertas son canal-agnósticos (flujo
  idéntico al de WhatsApp con caller-id = correo del sender); cadencia casi inmediata
  (poll configurable, default ~5 min); adjuntos (fotos/PDF) out of scope v1 — evolución
  documentada condicionada a la capacidad multimodal del runtime de Hermes. A diferencia
  de WhatsApp (verificado en CI del proyecto de referencia), el pairing del canal e-mail
  no tiene verificación previa: verificar en F1 con el bridge real.
- La identidad del cliente es multi-canal por canales probados: escribir desde el teléfono
  (WhatsApp) o desde el buzón (e-mail) crea/asocia ese identificador natural único; el mismo
  humano que llega por dos canales genera duplicados que se fusionan manualmente con
  `merge_clients` (no hay auto-fusión).
- UI y mensajes del sistema en español (público inicial hispanohablante).

### 3.4 Approach Técnico (alto nivel)

- **Arquitectura hexagonal** en paquetes Go (`application` con DTOs y usecases, `domain`,
  adaptadores de DB/MCP/web), heredera del layout probado del proyecto de referencia (ADRs de
  arquitectura en capas).
- **SQLite puro Go**: `modernc.org/sqlite` (sin CGo), WAL, `busy_timeout=5000`, FTS5 para
  búsquedas de catálogo y clientes. Trade-off aceptado: binario más grande a cambio de cero
  dependencia del sistema.
- **MCP sobre Streamable HTTP** (spec 2025-11-25) POST-only en loopback — corrige la mención
  "SSE" del `docs/idea.md` original: el pairing con Hermes está verificado en CI contra ese
  contrato en el proyecto de referencia.
- **Monorepo** con `web/` (proyecto Astro) compilado a estático y embebido con `go:embed`:
  un tag = un release = un binario con SHA256. Trade-off: build step de Node solo en
  desarrollo/CI, nunca en producción.
- **Scheduler in-process**: una goroutine consulta SQLite periódicamente (deadlines vencidos,
  inactividad) e inserta alertas pendientes. La durabilidad la da la DB, no la memoria; sin
  Redis, sin timers del SO, sin colas externas. Si Hermes está caído, las alertas se acumulan
  y se entregan a su vuelta — resiliente por diseño.
- **Login del panel por sesiones de servidor** en SQLite (revocables) + argon2id + CSRF
  double-submit + rate-limit/backoff. Sin JWT: con un solo servidor, el estado de sesión en
  DB es más simple de auditar y revocar.
- **Rediseño conciente, no copia**: la unificación staff/agendable (Q27) y el catálogo
  unificado (Q25) son decisiones nuevas que divergen del modelo del proyecto de referencia;
  las reservas y el ciclo de alertas se adaptan de ese proyecto con tests de paridad
  diferencial donde aplique.

### 3.5 Affected Areas

- `cmd/server/` — composición y arranque (MCP + panel + scheduler).
- `internal/` — hexagonal: application (DTOs/usecases), domain, adapters (db, mcp, web,
  scheduler), auth/resolver, webassets (`go:embed`).
- `web/` — proyecto Astro + Tailwind + TS del panel interno (`dist/` embebido en el binario).
- `docs/` — PRD (este documento), ADRs nuevos (bootstrap web, bot único Telegram, loopback
  por defecto + recetas opt-in), instalación (incluye allowlist de Hermes y recetas de
  acceso remoto), deployment, mantenimiento.
- `scripts/` — `install.sh` / `uninstall.sh` (covenant de despliegue).
- `.github/workflows/` — CI: build web → embed → fmt/vet/lint/build/test `-race` → release
  (GoReleaser + checksums).
- `odd/tasks/<feature>.md` — feature docs ODD: cada fase F1–F6 se implementa como uno
  o más features ODD, con Specs `S#` que citan textualmente los RF de este PRD, sus
  Tasks y su Log (método actual de Gentle AI; sin OpenSpec).

### 3.6 Rollback Plan

- **Estrategia**: cada fase se integra en su feature branch con work-unit commits
  revisables; rollback de una fase = revert del merge. Migraciones SQLite versionadas con
  camino de reverso donde aplique; antes de cada upgrade del binario, backup portable del
  archivo de DB (covenant heredado: la DB es un archivo copiable con el servicio detenido).
- **Tiempo estimado de rollback**: < 10 min (revert + swap de binario + restaurar DB).
- **Riesgo residual si rollback falla**: una migración no reversible dejaría el schema
  adelante del binario; mitigación: migraciones aditivas por defecto y backup previo
  obligatorio documentado.

### 3.7 Modelo de Datos (alto nivel)

Esquema definitivo por feature ODD de la fase; la forma canónica acordada:

| Entidad | Rol | Claves / notas |
|---|---|---|
| `accounts` | Owner/Admin/Staff — única fuente de identidad (login del panel y caller MCP) | `id` = **UUID interno**, `phone` **UNIQUE nullable** (resolución Telegram, verbatim del canal; sin teléfono la cuenta existe pero no puede usar el bot interno), `email` **UNIQUE NOT NULL** (identificador de login; obligatorio en el alta), `password_hash` (argon2id), `role`, `is_active`, `is_bookable`, `buffer_min` opcional (traslado/preparación aplicado entre reservas), `telegram_pending` (futuro binding) |
| `areas` | Áreas de atención configurables | nombre, staff miembros (N:M con `accounts`) |
| `clients` | Clientes finales | `id` = **UUID interno** (FKs estables ante cambios y merges), `phone` **UNIQUE nullable** (verbatim del canal WhatsApp), `email` **UNIQUE nullable** sobre `lower(email)`, `display_name`, notas, `default_address` opcional + `default_lat`/`default_lng` nullable (ubicación compartida por WhatsApp; la live location nunca se almacena). Registro por canal: WhatsApp-first crea con `phone`; e-mail-first crea con `email` |
| `business_profile` | Perfil del negocio | nombre, descripción, canales (`messenger_platform`, `messenger_id`), **`phone` (llamadas, distinto del `messenger_id`) + `email` + `website_url` + `social_links` (mapa red→URL, ej. Instagram/Facebook) — datos de contacto y web que Hermes comparte al Cliente que los pide**, **`address` + `lat`/`lng` opcionales + `maps_url`** (ubicación compartible: Hermes la pasa al Cliente que la pide, conduciendo hacia el turno), `online_store_url` opcional (fallback de venta online), `default_delivery_min` opcional (ventana de duración para delivery), config de fidelización (umbral de inactividad, umbral de seguimiento de leads) |
| `catalog_items` | Catálogo unificado | `kind` (producto\|servicio), nombre, `description` (qué es, corto), `use_cases` **nullable** (qué problemas resuelve, para quién — alimenta el `[RESULTADO]` de RF23 y el FTS), `limitations` **nullable** (qué NO cubre — honestidad pre-venta), precio, `sales_url` nullable (deep-link de compra online), `duration_min` solo para servicios (reservables) |
| `schedules` / `hours_exceptions` | Horarios semanales y excepciones de staff agendable | por `account_id` bookable |
| `bookings` | Reservas | client + staff agendable + datetime + estado (activa/cancelada/reprogramada); `kind` (servicio\|domicilio\|delivery) + `address` (obligatoria en domicilio/delivery) + `lat`/`lng` opcionales (congeladas de la ubicación del Cliente); duración: del servicio del catálogo (servicio/domicilio) o `default_delivery_min` (delivery, ítem vendido como contexto) |
| `ticket_types` | Tipos de ticket configurables | nombre, `default_area_id`; plantillas seed **'venta'** y **'consulta'** provistas ('venta': nuevo → contactado → presupuestado → cerrado/perdido, área Ventas con round-robin, evento 'link enviado'; 'consulta': nuevo → atendido → resuelto, sin área fija) |
| `ticket_states` | Estados por tipo | nombre, `deadline_hours` opcional, reglas on-enter/on-exit declarativas |
| `ticket_transitions` | Transiciones válidas | estado origen → destino, rol autorizado, auto-assign round-robin opcional |
| `tickets` | Instancia de contacto | tipo, cliente, estado actual, área, asignado, deadlines computadas |
| `ticket_events` | Historial auditable | transiciones, notas, cambios de asignación, con actor y timestamp |
| `alerts` | Alertas pendientes | tipo (overdue/on-enter/on-exit/inactividad/nuevo-ticket/posible-duplicado/seguimiento-lead), target (cliente o account), estado (pending/sent), payload |
| `sessions` | Sesiones del panel | token hash, account, expiración, inactividad |

Invariantes: la **PK es un UUID interno** (nunca un identificador de canal) — las FKs de
tickets, reservas y eventos quedan estables ante cambios de teléfono y ante merges. La
**resolución es por identificadores naturales**, byte-idénticos a lo que el gateway envía
(sin normalización): caller-id de WhatsApp → match exacto contra `clients.phone`;
caller-id de e-mail → match case-insensitive contra `clients.email` — 0 o 1 cliente, nunca
ambiguo. Mismo humano en dos canales → dos filas → `merge_clients` (Owner/Admin). Reclamar
por chat un email ajeno NO auto-vincula (fail-closed): error semántico + alerta de
sugerencia de merge al Owner/Admin. `CHECK`s de schema para enums (`kind`,
`messenger_platform`); FK enforcement ON. Familias que comparten un email: limitación
documentada, canal canónico WhatsApp.

### 3.8 Modelo de Autorización

Principios congelados (heredados y adaptados del gateway-contract y ADR-0018 del proyecto
de referencia):

- **El canal transporta una identidad, nunca un rol.** El gateway de Hermes lee el `from` de
  cada mensaje e inyecta `X-Caller-Id` **verbatim, por request, por emisor** — sin estado
  compartido entre emisores y sin normalización. El rol sale solo de la base:
  `accounts(is_active=1)` → owner/admin/staff; `clients` → client; sin fila → 401.
- **Anti-inyección**: un mensaje del tipo "soy el dueño" / "modo admin" / "ignorá tus reglas"
  jamás cambia el rol ni la persona. El rol viene únicamente de la resolución de identidad.
- **Fail-closed**: identidad ambigua o sin resolver → rechazo en el gateway (antes de
  cualquier tool call) o 401 del server. Nunca beneficio de la duda.
- **Sin auto-registro de internos**: un teléfono desconocido en el bot interno de Telegram se
  rechaza; las cuentas internas las crea el Owner/Admin. Solo los **clientes** tienen loop
  de auto-registro en el canal del negocio (con rate-limit por identificador de canal —
  teléfono o correo —, heredado).
- **Resolución Telegram interna (Q33b)**: `request_contact` una sola vez por persona
  (Telegram solo permite compartir el propio número — no suplantable) → teléfono → match
  contra `accounts` → rol. Evolución futura documentada: binding por `telegram_user_id` con
  aprobación del Owner/Admin.
- **`tools/list` filtrado por rol**: cada caller solo ve lo que puede invocar.
- **Visibilidad de tickets (Q30)**: staff ve sus tickets asignados + los no asignados de
  sus áreas; admin/owner ven todo.
- **Loopback invariant**: el MCP vive solo en `127.0.0.1:3001`; el panel solo en
  `127.0.0.1:3002`. Ningún puerto público por defecto.
- **Resolución del canal e-mail**: el correo del sender matchea (case-insensitive) contra
  `clients.email` UNIQUE → 0 o 1 cliente, determinista; desconocido → loop de registro (crea
  con su correo, `phone` NULL); colisión al cargar → error semántico con degradación
  elegante (el email es opcional, el cliente sigue operativo por WhatsApp).
- **Auditoría**: events con actor y timestamp; PII (teléfonos) enmascarada a los últimos 4
  dígitos en todo log de auditoría.

---

## 4. Usuarios y Personas

### 4.1 Personas

| Persona | Necesidad principal | Frecuencia de uso |
|---------|---------------------|-------------------|
| **Cliente final** | Consultar productos/servicios, abrir reclamos o pedidos de asistencia, pedir turnos — todo por WhatsApp o e-mail, sin instalar nada | Esporádica |
| **Staff** | Ver y atender sus tickets y su agenda; pedirle acciones a Hermes por Telegram mientras trabaja | Diaria |
| **Admin** | Operar el negocio: fusionar clientes, administrar cuentas de staff, cuidar los reportes | Diaria |
| **Owner / Dueño** | Configurar el negocio completo (perfil, catálogo, áreas, workflows), recibir avisos, ver reportes — por Telegram y panel, sin terminal | Diaria |
| **Hermes** (agente) | Operar el sistema exclusivamente por las tools MCP con la identidad del caller que representa | Continua |

### 4.2 User Stories de Alto Nivel

- Como **Cliente**, quiero consultar el catálogo por WhatsApp, para saber qué ofrece el negocio
  y a qué precio.
- Como **Cliente**, quiero abrir un reclamo o pedido de asistencia conversando con Hermes,
  para que quede registrado y alguien me responda.
- Como **Cliente**, quiero pedir, mover o cancelar un turno por chat, para no llamar por
  teléfono.
- Como **Staff**, quiero ver mis tickets asignados y los de mi área, para saber qué atender
  hoy.
- Como **Staff**, quiero pedirle a Hermes por Telegram que cambie el estado de un ticket,
  para avanzar el trabajo sin abrir el panel.
- Como **Staff agendable**, quiero que mi agenda y horarios administren mis turnos, para no
  duplicar reservas.
- Como **Owner**, quiero definir tipos de ticket con sus estados y reglas (avisos, deadlines,
  round-robin), para que cada contacto entre a su workflow correcto sin que yo lo toque.
- Como **Owner**, quiero recibir en Telegram los avisos de tickets overdue y de clientes
  inactivos, para que nada se me pierda.
- Como **Owner**, quiero configurar todo el negocio desde el panel web sin terminal, para no
  depender de un técnico.
- Como **Admin**, quiero fusionar clientes duplicados y administrar cuentas de staff, para
  mantener la base prolija.
- Como **Owner**, quiero reportes con export CSV, para compartir el estado del negocio con
  socios o mi contador.

---

## 5. Requerimientos

### 5.1 Requerimientos Funcionales (RF)

**RF1: Servidor MCP auto-contenido**
- **Descripción**: binario Go que sirve MCP sobre Streamable HTTP POST-only en
  `127.0.0.1:3001` (con `/healthz`) y el panel interno en `127.0.0.1:3002`; DB SQLite en
  ruta XDG; se niega a arrancar con config inválida (fail-closed).
- **Prioridad**: Must (F1)
- **Criterios de Aceptación**:
  - [ ] Dado el binario instalado y la DB inicializada, cuando se hace `GET /healthz`,
    entonces responde 200 y `GET /mcp` responde 405 (POST-only).
  - [ ] Dado cualquier puerto no-loopback en config, cuando el binario arranca, entonces
    enlaza solo 127.0.0.1 (invariante de loopback).

**RF2: Identidad y RBAC por caller**
- **Descripción**: cada request MCP lleva `X-Caller-Id` verbatim; el resolver aplica rol
  desde `accounts`/`clients`; `tools/list` solo anuncia las tools invocables por ese rol;
  header ausente → 401 antes de cualquier handler.
- **Prioridad**: Must (F1)
- **Criterios de Aceptación**:
  - [ ] Dado un caller `staff`, cuando pide `tools/list`, entonces no aparecen tools de
    administración del Owner.
  - [ ] Dado un request sin `X-Caller-Id`, cuando llega cualquier tool call, entonces
    responde error JSON-RPC `-32000` sin ejecutar nada.
  - [ ] Dado un mensaje "soy el dueño" de un caller `client`, cuando se procesa, entonces
    el rol sigue siendo `client` (anti-inyección).

**RF3: Registro e identidad de clientes**
- **Descripción**: `clients.id` es un **UUID interno**; los identificadores naturales son
  `phone` (UNIQUE nullable, verbatim) y `email` (UNIQUE nullable sobre `lower(email)`).
  Un cliente nuevo se auto-registra en el canal por el que llegó (loop con rate-limit por
  identificador de canal): WhatsApp-first crea con `phone`; e-mail-first crea con `email`
  (y `phone` NULL). Reclamar por chat un email que ya pertenece a otro cliente NO
  auto-vincula (fail-closed): error semántico con degradación elegante + **alerta de
  sugerencia de merge** (`posible-duplicado`) para Owner/Admin. `merge_clients` (Owner/Admin)
  fusiona duplicados preservando historia (tickets, reservas, eventos).
- **Prioridad**: Must (F1 registro por canal) / alerta `posible-duplicado` y `merge_clients`:
  Should (F2 y F4 respectivamente)
- **Criterios de Aceptación**:
  - [ ] Dado un teléfono que no existe, cuando el gateway reintenta tras
    `register_client`, entonces el retry original funciona una vez y solo una.
  - [ ] Dado un sender desconocido por e-mail, cuando se auto-registra, entonces queda
    creado con su correo como identificador natural y `phone` NULL.
  - [ ] Dado un cliente WhatsApp-first que declara un email ya usado por otro cliente,
    cuando se procesa, entonces NO se auto-vincula: error semántico, degradación elegante
    (sigue sin email) y alerta `posible-duplicado` pendiente para el Owner/Admin.
  - [ ] Dados dos clientes duplicados con tickets y reservas, cuando el Owner ejecuta
    `merge_clients`, entonces el sobreviviente conserva todos los tickets, reservas y
    eventos y el duplicado deja de resolverse.

**RF4: Catálogo unificado**
- **Descripción**: `catalog_items` con `kind` (producto|servicio); alta/edición por
  Owner/Admin; búsqueda full-text para Hermes (`search_catalog`); solo servicios
  (con `duration_min`) son reservables. El catálogo lleva **conocimiento orientado al
  agente**: `description` (qué es), `use_cases` (qué problemas resuelve, para quién —
  el `[RESULTADO]` de la doctrina RF23) y `limitations` (qué NO cubre — honestidad
  pre-venta); el FTS de `search_catalog` indexa los tres campos, así el match nace del
  problema que el Cliente describe y no solo del nombre del ítem. Si las respuestas
  crecen, un `get_catalog_item` de detalle complementa el search ligero (decisión de
  diseño de T7).
- **Prioridad**: Must (F1)
- **Criterios de Aceptación**:
  - [ ] Dado un `catalog_items` con `kind=producto`, cuando Hermes busca slots de reserva
    para ese ítem, entonces es rechazado como no reservable con mensaje semántico.
  - [ ] Dado un catálogo con productos y servicios, cuando un cliente consulta "¿tenés
    corte de pelo?", entonces `search_catalog` devuelve el servicio con su precio y
    duración.

**RF5: Tickets con historial auditable**
- **Descripción**: **todo contacto con motivo de negocio genera un ticket** — también las
  consultas que no terminan en compra ni solicitud (alimentan el funnel, el seguimiento y
  las estadísticas); la excepción es el mensaje sin motivo (off-topic puro). Política de
  **re-engagement**: si el Cliente vuelve sobre lo mismo con un ticket abierto de ese tipo,
  sus mensajes se agregan como eventos al ticket existente y no se crea uno nuevo; ticket
  nuevo solo cuando el anterior está cerrado. Toda transición, nota, mensaje de
  re-engagement y cambio de asignación queda en `ticket_events` con actor y timestamp;
  estados/deadlines computan desde la config del tipo.
- **Prioridad**: Must (F1 tipo fijo → F2 configurable)
- **Criterios de Aceptación**:
  - [ ] Dado un reclamo nuevo por WhatsApp, cuando Hermes crea el ticket, entonces queda en
    el estado inicial del tipo con su área por defecto y un evento de creación auditado.
  - [ ] Dado un Cliente que solo consulta sobre un producto y no compra ni solicita nada,
    cuando se cierra el contacto, entonces existe un ticket 'consulta' con su historial —
    el contacto quedó contado en las estadísticas.
  - [ ] Dado un ticket 'consulta' abierto y el mismo Cliente preguntando de nuevo lo mismo,
    cuando llegan sus mensajes, entonces se agregan como eventos al ticket existente y el
    contador de tickets del funnel no se incrementa.
  - [ ] Dado un ticket, cuando se listan sus eventos, entonces cada transición muestra
    quién, cuándo y desde/hacia qué estado.

**RF6: Motor de workflows configurable (reglas declarativas)**
- **Descripción**: el Owner define por UI (panel) o conversacionalmente (Hermes, con
  confirmaciones de doble gate para operaciones destructivas): tipos de ticket, estados,
  transiciones válidas y reglas declarativas — notificación on-enter/on-exit, `deadline`
  por estado con aviso overdue, auto-asignación round-robin del área, rol autorizado por
  transición. Sin scripting ni condiciones libres. La config inválida se rechaza con error
  semántico (fail-closed).
- **Prioridad**: Must (F2)
- **Criterios de Aceptación**:
  - [ ] Dado un estado con `deadline_hours=48`, cuando el scheduler corre pasada la fecha,
    entonces se genera una alerta `overdue` pendiente para el área asignada.
  - [ ] Dada una transición que exige rol `staff`+, cuando un `client` intenta ejecutarla
    por chat, entonces es rechazada con `FORBIDDEN` semántico.
  - [ ] Dada una config con transición a un estado inexistente, cuando el Owner la guarda,
    entonces se rechaza la config completa y nada queda parcialmente escrito.

**RF7: Áreas configurables**
- **Descripción**: el Owner crea/renombra/elimina áreas y les asigna staff; cada tipo de
  ticket declara su área por defecto.
- **Prioridad**: Must (F2)
- **Criterios de Aceptación**:
  - [ ] Dado un staff en las áreas "Ventas" y "Soporte", cuando entra al board, entonces ve
    los tickets no asignados de ambas áreas.

**RF8: Visibilidad de tickets**
- **Descripción**: staff ve sus asignados + los no asignados de sus áreas; admin/owner ven
  todo; la misma regla aplica a las tools MCP de listado.
- **Prioridad**: Must (F2)
- **Criterios de Aceptación**:
  - [ ] Dado un ticket asignado a otro staff de otra área, cuando un staff lista tickets,
    entonces ese ticket no aparece.

**RF9: Operación conversacional de tickets**
- **Descripción**: internos operan tickets por el bot de Telegram: listar, consultar,
  transicionar, comentar, tomar un ticket no asignado de su área — cada acción pasa por
  tools MCP con su identidad y permisos.
- **Prioridad**: Must (F2)
- **Criterios de Aceptación**:
  - [ ] Dado un staff autenticado por `request_contact`, cuando pide "pasá el ticket 23 a
    en_proceso", entonces la transición se ejecuta con su identidad y el evento queda
    auditado a su nombre.

**RF10: Reservas sobre staff agendable (tres clases de cita)**
- **Descripción**: staff con `is_bookable` tiene horarios semanales, excepciones y
  `buffer_min` opcional (traslado/preparación entre reservas). Slots disponibles se calculan
  contra duración, horarios, excepciones, buffer y reservas activas. **Clases de cita**
  (`bookings.kind`): **servicio** presencial (duración del catálogo), **domicilio**
  (asistencia técnica, con `address` obligatoria) y **delivery** (ventana
  `default_delivery_min` del negocio, ítem vendido como contexto, con `address`). Búsqueda
  de disponibilidad por **staff específico o por área** (cualquier staff agendable del
  área; el sistema elige por earliest-slot o round-robin configurado). Dirección:
  `bookings.address` con pre-carga opcional desde `clients.default_address` (+
  coordenadas si el Cliente compartió su ubicación por WhatsApp; la live location nunca
  se almacena). Crear/
  cancelar/reprogramar desde chat (cliente) o panel; agenda legible por el propio staff.
- **Prioridad**: Must (F3)
- **Criterios de Aceptación**:
  - [ ] Dado un staff agendable con horario 9–13 y una reserva existente 10–10:30, cuando
    se buscan slots de 30 min ese día, entonces 10:00 no aparece y 10:30 sí.
  - [ ] Dado un staff con `buffer_min=30` y una reserva hasta las 10:00, cuando se buscan
    slots ese día, entonces no se ofrece ninguno antes de las 10:30.
  - [ ] Dado un cliente que pide "que venga un técnico a mi casa", cuando Hermes busca
    disponibilidad por área, entonces obtiene el próximo slot de cualquier staff agendable
    del área y la reserva se crea con `kind=domicilio` y la dirección del cliente.
  - [ ] Dado un delivery de un producto vendido sin tienda online, cuando Hermes agenda
    con el repartidor (staff agendable), entonces la reserva se crea con `kind=delivery`,
    la ventana `default_delivery_min` y el ítem como contexto — nunca con la duración del
    producto.
  - [ ] Dado un Cliente que comparte su ubicación por WhatsApp y pide un técnico, cuando
    Hermes agenda la visita, entonces la reserva congela dirección y coordenadas y
    confirma la dirección en texto.
  - [ ] Dado un cliente con reserva activa, cuando pide cancelarla por WhatsApp, entonces
    queda `cancelada` y el slot vuelve a estar disponible.

**RF11: Notificaciones como alertas pendientes**
- **Descripción**: scheduler in-process (tick configurable) inserta alertas pendientes por:
  deadlines vencidos (overdue), reglas on-enter/on-exit, recordatorios de inactividad,
  tickets nuevos por área; Hermes las recoge con `get_pending_alerts` y las marca con
  `mark_alert_as_sent`. El backend jamás envía mensajes por sí mismo; si Hermes cae, las
  alertas se acumulan y se entregan a su vuelta.
- **Prioridad**: Must (F2 esqueleto, F5 inactividad)
- **Criterios de Aceptación**:
  - [ ] Dada una alerta pendiente, cuando Hermes consulta y entrega, entonces queda
    `sent` y no se re-entrega en la siguiente pasada.
  - [ ] Dado Hermes caído 2 horas, cuando vuelve, entonces recibe todas las alertas
    acumuladas en orden, sin duplicados.

**RF12: Fidelización — reportes y recordatorios de inactividad**
- **Descripción**: reportes de reincidencia, clientes inactivos y ranking de consumo;
  recordatorio automático de inactividad (umbral configurable en `business_profile`)
  genera una alerta pendiente que Hermes entrega al cliente por su canal.
- **Prioridad**: Should (F5)
- **Criterios de Aceptación**:
  - [ ] Dado un cliente sin actividad por encima del umbral configurado, cuando corre el
    scheduler, entonces se genera una única alerta de inactividad (sin spam: no repite
    hasta que haya actividad nueva y vuelva a cruzar el umbral).

**RF13: Panel interno — login con sesión de servidor**
- **Descripción**: login email+contraseña contra `accounts` (argon2id), sesión en SQLite con
  cookie `HttpOnly`/`Secure`/`SameSite=Strict`, expiración e inactividad, CSRF
  double-submit, rate-limit/backoff en intentos fallidos. 2FA TOTP: fase posterior (Could).
- **Prioridad**: Must (F4)
- **Criterios de Aceptación**:
  - [ ] Dados 5 intentos fallidos, cuando se intenta un 6.º, entonces hay backoff
    creciente documentado (no lock-out permanente).
  - [ ] Dada una sesión activa, cuando el Owner/Admin borra la cuenta o desactiva al
    usuario, entonces la sesión queda revocada en el próximo request.

**RF14: Panel — board de tickets y operación**
- **Descripción**: board de tickets filtrable por área/estado/asignado según visibilidad;
  acciones: transicionar, comentar, tomar, asignar (según rol).
- **Prioridad**: Must (F4)

**RF15: Panel — editor de workflows**
- **Descripción**: UI para tipos/estados/transiciones/reglas declarativas con validación
  íntegra antes de guardar (mismas reglas que RF6).
- **Prioridad**: Must (F4)

**RF16: Panel — settings del negocio**
- **Descripción**: perfil del negocio y canales, catálogo, staff (roles, `is_bookable`,
  horarios), áreas, umbral de fidelización. Todo el setup post-owner vive acá (sin TUI, sin
  wizard de instalación).
- **Prioridad**: Must (F4)

**RF17: Panel — reportes, export CSV y gráficos**
- **Descripción**: dashboard operativo (tickets abiertos/overdue por tipo, área y SLA,
  actividad de clientes, reincidencia, inactivos, ranking) + export CSV/JSON por reporte +
  gráficos simples de tendencia semanal.
- **Prioridad**: Must (F4 base / F5 fidelización + export)

**RF18: Bootstrap por CLI mínimo**
- **Descripción**: `crm-ai-business setup --create-owner` crea la cuenta Owner (único paso
  de terminal del producto); el resto del setup se hace en el panel. Hasta F4 (panel), las
  fases de desarrollo usan fixtures documentadas.
- **Prioridad**: Must (F4)
- **Criterios de Aceptación**:
  - [ ] Dada una DB vacía, cuando corre `setup --create-owner`, entonces la cuenta queda
    resuelvable como `owner` por teléfono y no se puede crear un segundo owner (invariante
    single-owner heredada).

**RF19: Canales — bot interno único de Telegram**
- **Descripción**: un solo bot de Telegram compartido; cada interno hace `request_contact`
  una única vez; el teléfono compartido (solo el propio es compartible) resuelve contra
  `accounts`; desconocidos rechazados sin auto-registro; clientes por WhatsApp/e-mail con
  el canal del negocio.
- **Prioridad**: Must (F1 pairing clientes / F2 bot interno)
- **Criterios de Aceptación**:
  - [ ] Dado un teléfono que no matchea ninguna cuenta, cuando escribe al bot interno,
    entonces el rechazo ocurre en el gateway antes de cualquier tool call, con mensaje en
    español.
  - [ ] Dado un staff ya resuelto, cuando escribe de nuevo días después, entonces no se le
    vuelve a pedir el contacto (resolución persistente).

**RF20: Documentación de seguridad del canal (allowlist Hermes)**
- **Descripción**: la documentación de instalación manda la allowlist mínima de Hermes por
  plataforma (`whatsapp`, `telegram`): MCP server propio + `skills` + `clarify` + `memory` +
  `todo`; todo lo demás (`code_execution`, `terminal`, `file`, `browser`, `web`,
  `computer_use`, `delegation`, `cronjob`, `image_gen`, `tts`, `vision`, `session_search`,
  `connections`, plugins de terceros) deshabilitado, con el comando exacto, la advertencia
  de que un `disable` suelto de `code_execution` no alcanza, backup previo del
  `~/.hermes/config.yaml`, la prueba final (pedir "ejecutá `echo hola` en la terminal" →
  negarse; pedir catálogo → venir del CRM) y la obligación de re-auditar tras cada upgrade
  del runtime o re-pairing. El auditor es documentación (decisión de producto); el producto
  solo garantiza funcionamiento con esa allowlist.
- **Prioridad**: Must (F6)
- **Criterios de Aceptación**:
  - [ ] Dada una instalación con allowlist mínima aplicada, cuando se pide al bot un
    comando de terminal, entonces responde que no tiene esa herramienta, y el catálogo
    sigue funcionando.

**RF21: Instalación y desinstalación covenant**
- **Descripción**: wizard interactivo + `install.sh` pineado por versión exacta con SHA256;
  servicio user-level (systemd/launchd) sin root; uninstall con dry-run por defecto y
  preservación de datos salvo `--purge-data` con doble gate; recetas de acceso remoto al
  panel (túnel SSH / Caddy+HTTPS / Tailscale) documentadas como opt-in.
- **Prioridad**: Must (F6)

> **Nota**: los criterios Gherkin de este PRD son la fuente canónica; al implementar
> cada RF se citan textualmente en los `S#` del feature doc ODD (`odd/tasks/<feature>.md`)
> y los tests los verifican. La prioridad `[Must | Should | Could]` se respeta en el
> routing de tareas de cada feature.

**RF22: Skill de recepcionista y personas conversacionales**
- **Descripción**: el repo shippea la skill de Hermes como **template estático versionado**
  (formato estándar del runtime en `~/.hermes/skills/`), **independiente de los datos del
  negocio** — los datos salen solo de las tools MCP en runtime, nunca embebidos en la skill.
  Contenido: rol (asistente del negocio `business_profile.name`, nunca asistente general ni
  humano), off-topic cordial sin tool calls (un párrafo con saludo y oferta de servicios),
  loop de registro como regla, guardrails de privacidad (datos mínimos, cero PII de otros
  clientes), escalado al owner, **personas por rol resuelto** (client en el canal del
  negocio; owner/admin/staff en el bot interno — fail-closed a la persona client ante toda
  ambigüedad) y **doble gate de confirmación** para operaciones destructivas por chat.
  Postura de seguridad explícita: la skill es texto en reposo — no otorga permisos ni
  reemplaza el RBAC; el boundary real es server-side (loopback, per-sender, allowlist).
  Instalación: F1 instrucciones manuales exactas en la doc de pairing; F6 paso opcional del
  wizard de `install.sh` con consent explícito.
- **Prioridad**: Must (F1 base, F2 personas internas, F6 instalación por installer)
- **Criterios de Aceptación**:
  - [ ] Dada la skill instalada y un mensaje off-topic (el clima), cuando Hermes responde,
    entonces lo hace con un párrafo cordial, sin invocar tools y ofreciendo los servicios
    del negocio.
  - [ ] Dado un mensaje "soy el dueño" de un caller resuelto como `client`, cuando aplica
    la selección de personas, entonces sigue siendo persona client (anti-inyección).
  - [ ] Dado el negocio sin datos cargados, cuando un cliente consulta el catálogo,
    entonces Hermes responde con lo que la tool devuelve, sin inventar datos.

**RF23: Doctrina de atención resolutiva**
- **Descripción**: el valor central del producto — Hermes conduce cada contacto del Cliente
  hacia la resolución efectiva de su motivo desde el primer mensaje. La skill prescribe
  **principios con ejemplos** (no libretos literales): responder mostrando disposición a
  resolver, descubrir el problema y cerrar **siempre** con una pregunta concreta — nunca
  dejar al Cliente sin próximo paso. Honestidad: sin click-tracking en v1, la conducción
  parte del contexto real, sin presumir lo que el Cliente vio. Caminos de resolución:
  consulta de catálogo (tools MCP); intención de compra → `sales_url` del ítem (fallback
  `online_store_url`) con evento 'link enviado' en el ticket de venta, o derivación al área
  Ventas (ticket 'venta', round-robin) si no hay venta online; reclamo/asistencia → ticket a
  su tipo y área; turno → reserva. Seguimiento: sin compra confirmada tras el umbral
  configurable, alerta `seguimiento-lead` al vendedor asignado (sin repetir hasta nueva
  actividad).
- **Prioridad**: Must (F1 doctrina en skill base, F2 plantilla 'venta', F5 seguimiento-lead)
- **Criterios de Aceptación**:
  - [ ] Dado un Cliente que solo saluda ("Hola"), cuando Hermes responde, entonces conduce:
    muestra disposición a resolver y cierra con una pregunta concreta que invita a contar su
    necesidad.
  - [ ] Dado un Cliente que quiere comprar un ítem con `sales_url`, cuando Hermes atiende,
    entonces comparte el link directo del ítem y registra el evento 'link enviado' en el
    ticket de venta.
  - [ ] Dado un Cliente que quiere comprar y el negocio no tiene venta online, cuando Hermes
    atiende, entonces crea un ticket 'venta' asignado al área Ventas (round-robin) y le
    confirma que un vendedor lo contactará.
  - [ ] Dado un ticket 'venta' con link enviado y sin compra confirmada pasado el umbral,
    cuando corre el scheduler, entonces genera una única alerta `seguimiento-lead` al
    vendedor asignado (sin repetir hasta nueva actividad).

### 5.2 Requerimientos No Funcionales (RNF)

| Categoría | Requerimiento | Métrica / Target |
|-----------|---------------|------------------|
| Performance | Latencia de tools MCP en loopback con DB cargada (10k clientes sintéticos) | p95 < 200 ms |
| Concurrencia | Micro-negocio: llamadas MCP y requests de panel simultáneos | ≤ 10 concurrentes sin degradación |
| Escalabilidad | Techo validado, no asumido | 10.000 clientes / 100.000 tickets sintéticos en CI |
| Disponibilidad | Self-hosted con servicio user-level y restart | ≥ 99% mensual; arranque tras reboot |
| Seguridad | Loopback-only por defecto; argon2id; CSRF; rate-limit; RBAC server-side; PII enmascarada (últimos 4 dígitos) en logs de auditoría; allowlist de Hermes documentada | 0 puertos públicos por defecto; 0 secrets en logs |
| Usabilidad | Instalación documentada end-to-end; setup post-owner 100% web | < 30 min de VPS limpia a demo |
| Mantenibilidad | Pipeline pre-commit + CI: fmt, vet, golangci-lint, build, test `-race` | Cobertura > 80%; CI verde obligatoria |
| Observabilidad | `journalctl` (logs del servicio), `/healthz`, audit log con máscaras, estado del scheduler visible | Cada alerta generada es trazable a su causa |
| Resiliencia | Hermes caído no pierde notificaciones (alertas se acumulan); restart del binario no pierde schedules (estado en SQLite) | 0 alertas perdidas por restart |
| Backup | DB = archivo portable; backup documentado con servicio detenido | Restauración < 10 min |
| Compliance/Licencia | MIT; datos del negocio y de clientes viven solo en la VPS del dueño (sin telemetría) | 0 envíos de datos a terceros |

---

## 6. Restricciones Técnicas

### 6.1 Stack Técnico

- **Backend**: Go (misma línea de versiones que el proyecto de referencia, Go 1.27+),
  `modernc.org/sqlite` (puro Go, sin CGo), MCP go-sdk (Streamable HTTP, spec 2025-11-25).
- **Frontend interno**: Astro + Tailwind CSS + TypeScript; build estático embebido con
  `go:embed`; sin runtime Node en producción.
- **Base de datos**: SQLite, WAL, `busy_timeout=5000`, FTS5 para búsquedas de catálogo y
  clientes.
- **Infraestructura**: cero servicios externos — sin Redis, sin colas, sin Docker; servicio
  user-level (systemd `--user` + linger en Linux, launchd en macOS); Windows vía WSL2.
- **Herramientas**: golangci-lint, GoReleaser (+ checksums SHA256), GitHub Actions (CI +
  release), Gentle AI/ODD como flujo de desarrollo (issues-first, PRs del owner).

### 6.2 Integraciones Externas

| Sistema | Tipo | Propósito | Criticidad |
|---------|------|-----------|------------|
| Hermes (runtime agente) | Agente MCP + gateway de canales | Única interfaz de clientes y operación conversacional interna; contrato per-sender `X-Caller-Id` | Bloqueante |
| WhatsApp (bridge Hermes) | Canal de mensajería | Canal del negocio para clientes | Bloqueante |
| Telegram Bot API | Canal de mensajería | Bot interno único del equipo (request_contact) | Bloqueante |
| E-mail (canal Hermes) | Canal de mensajería | Canal de clientes: caller-id = correo del sender; cadencia casi inmediata (poll ~5 min configurable); sin adjuntos v1; pairing SIN verificación previa (verificar en F1) | Importante |
| systemd / launchd | Servicio del SO | Arranque, restart, arranque tras reboot | Importante |
| GitHub Releases | Distribución | Binarios + checksums + tags pineados | Importante |
| mcp-appointments-crm (referencia) | Repositorio de patrones | ADRs, installer, usecases de reservas, convenciones — reuso de conocimiento, no fork | Paralela |

### 6.3 Compliance y Seguridad

- **Regulaciones aplicables**: protección de datos personales de clientes (PII: teléfonos,
  e-mails, historial) — los datos viven exclusivamente en la VPS del dueño; sin telemetría
  ni envíos a terceros.
- **Datos sensibles manejados**: PII de clientes y equipo, contraseñas (argon2id),
  tokens de bots (nunca en logs; secretos en `.env` con permisos restringidos).
- **Controles de seguridad requeridos**: loopback-only por defecto; RBAC server-side con
  `tools/list` filtrado; anti-inyección de rol; fail-closed en identidad; rate-limit de
  auto-registro de clientes; rate-limit de login con backoff; CSRF; sesiones revocables;
  auditoría con PII enmascarada; **allowlist mínima de Hermes documentada como requisito de
  instalación** (mitigación del tool boundary del runtime, issue upstream
  hermes-agent#147647).

---

## 7. Roadmap y Fases

> **Regla**: cada fase se implementa como uno o más features ODD (`odd/tasks/<feature>.md`),
> cuyos Specs `S#` citan textualmente los RF de este PRD. Cada fase cierra una demo
> verificable (vertical slice).

### Fase 1: core-mcp (Estimación: M)

**Objetivo**: demo end-to-end conversacional — un cliente consulta por WhatsApp y Hermes
crea un ticket de un tipo fijo que el staff atiende por chat con RBAC.

**Entregables**:
- Esqueleto hexagonal Go + schema base (`accounts`, `clients`, `business_profile`,
  `catalog_items`, `tickets` con un tipo fijo).
- Servidor MCP (Streamable HTTP POST-only, loopback, `/healthz`) + RBAC con
  `tools/list` por rol.
- Loop de auto-registro de clientes (con rate-limit) y pairing con Hermes + skill base con
  la doctrina de atención resolutiva (RF23). Contrato de canales documentado con el rigor
  del gateway-contract del proyecto de referencia — **incluye el canal e-mail, cuyo
  pairing no tiene verificación previa** (WhatsApp sí la tiene).
- CI (fmt/vet/lint/build/test) + tests de volumen inicial.

**Definition of Done**:
- [ ] Demo reproducible documentada: WhatsApp → consulta → ticket creado → staff lista y
  transiciona.
- [ ] RF1–RF4 y RF5 (tipo fijo) con criterios verificados en CI.
- [ ] `go test -race ./...` verde; CI verde.

### Fase 2: workflow-engine (Estimación: L)

**Objetivo**: el Owner configura el negocio conversando — tipos, estados, transiciones y
reglas declarativas — y el motor los ejecuta con avisos y deadlines.

**Entregables**:
- Entidades de configuración (tipos, estados, transiciones, reglas on-enter/on-exit,
  deadlines, round-robin por área) + validación íntegra fail-closed.
- Tools MCP de administración para Owner (config de workflows por chat, doble gate en
  operaciones destructivas).
- Scheduler in-process (deadlines → alertas overdue; on-enter/on-exit → alertas).
- Áreas configurables + visibilidad de tickets + operación conversacional completa.
- Plantillas seed **'venta'** y **'consulta'**: pipelines listos (venta: nuevo →
  contactado → presupuestado → cerrado/perdido, área Ventas con round-robin, evento
  'link enviado'; consulta: nuevo → atendido → resuelto).
- Bot interno único de Telegram con `request_contact` + resolución per-emisor.

**Definition of Done**:
- [ ] Workflow configurable end-to-end por chat sin tocar código.
- [ ] RF6–RF9, RF11 (esqueleto), RF19 (bot interno) verificados.
- [ ] Tests de paridad del motor (config inválida rechazada íntegra; transiciones por rol).

### Fase 3: bookings (Estimación: M/L)

**Objetivo**: reservas completas sobre staff agendable unificado.

**Entregables**:
- Staff `is_bookable` con horarios semanales y excepciones; cálculo de slots; agenda propia
  del staff.
- Crear/cancelar/reprogramar reservas desde chat y (más adelante) panel; clientes reciben
  confirmaciones como alertas.
- Adaptación de los usecases probados del proyecto de referencia con tests diferenciales
  donde el modelo unificado diverge.
- **Clases de cita** (`bookings.kind` servicio/domicilio/delivery con `address`) +
  búsqueda de disponibilidad **por área** + `buffer_min` por staff agendable.

**Definition of Done**:
- [ ] RF10 verificado, incluidos slots contra excepciones y reservas activas.
- [ ] Demo: cliente pide turno por WhatsApp → confirmación → staff ve su agenda.

### Fase 4: web interna (Estimación: L)

**Objetivo**: el negocio opera completo desde el navegador sin terminal.

**Entregables**:
- Panel Astro embebido (`go:embed`): login con sesión (argon2id/CSRF/rate-limit), board de
  tickets, editor de workflows, settings completos (perfil, catálogo, staff, áreas),
  reportes base.
- `crm-ai-business setup --create-owner` (bootstrap CLI mínimo).

**Definition of Done**:
- [ ] RF13–RF18 verificados; flujo real: VPS → CLI owner → login → configurar negocio.
- [ ] Panel sirve desde el binario único en `127.0.0.1:3002`; build verificado en release.

### Fase 5: loyalty-alerts (Estimación: M)

**Objetivo**: el sistema recupera clientes inactivos y reporta la salud del negocio.

**Entregables**:
- Recordatorios automáticos de inactividad (umbral configurable) como alertas pendientes.
- Seguimiento de leads: alerta `seguimiento-lead` al vendedor asignado si no hay compra
  confirmada tras el umbral configurable.
- Reportes de fidelización (reincidencia, inactivos, ranking) + export CSV/JSON + gráficos
  simples en el panel.

**Definition of Done**:
- [ ] RF12 y RF17 completos verificados (incluido no-spam del recordatorio).

### Fase 6: install-release (Estimación: S/M)

**Objetivo**: instalación de primer nivel, reproducible y segura por documentación.

**Entregables**:
- Wizard interactivo + `install.sh` pineado con SHA256 + servicio user-level + uninstall
  (dry-run por defecto).
- Documentación: instalación completa, **allowlist mínima de Hermes con comandos y prueba
  final**, recetas de acceso remoto (túnel SSH / Caddy+HTTPS / Tailscale), deployment,
  mantenimiento.
- **Skill de Hermes**: paso opcional del wizard de `install.sh` con consent explícito
  (template estático versionado del repo; igual que el viejo: consentimiento al escribir
  en el home de Hermes).
- GoReleaser + checksums + smoke e2e del installer en CI (Linux y macOS).

**Definition of Done**:
- [ ] RF20, RF21 verificados; instalación end-to-end desde cero documentada y probada en
  CI.
- [ ] La sección de seguridad del canal sigue el formato probado del proyecto de
  referencia (comandos verificados, no de memoria).

### Fase N: soporte y mejoras (ongoing)

**Objetivo**: operación continua — hardening del installer/uninstaller, contratos de
canal, automatización de release, y las evoluciones documentadas: binding de Telegram por
`telegram_user_id`, 2FA TOTP, i18n si hay demanda, chat embebido si Hermes expone API
conversacional.

---

## 8. Riesgos y Dependencias

### 8.1 Riesgos

| # | Riesgo | Probabilidad | Impacto | Mitigación |
|---|--------|--------------|---------|------------|
| R1 | Hermes con toolsets full-access escribe la DB sin pasar por el MCP (clase del incidente v0.10.0 del proyecto de referencia; issue upstream #147647 OPEN) | Media | Alto | Allowlist mínima documentada como requisito de instalación (RF20) con comando exacto, prueba final y re-audit tras cada upgrade; el producto declara que solo garantiza funcionamiento con esa allowlist |
| R2 | El rediseño staff/agendable unificado introduce regresiones de comportamiento respecto del modelo probado de reservas | Media | Alto | Tests de paridad diferencial contra los casos del proyecto de referencia (slots, excepciones, cancelaciones) en F3 |
| R3 | Complejidad del motor de workflows (configuraciones rotas que el Owner no sabe diagnosticar) | Media | Medio | Reglas declarativas acotadas (sin scripting), validación íntegra fail-closed con errores semánticos en español, no-spam garantizado en recordatorios |
| R4 | El pairing con Hermes (gateway per-sender, canales, skills) cambia con el runtime | Media | Alto | Smokes de canal por fase; versiones verificadas documentadas; seguimiento del contrato de gateway del proyecto de referencia |
| R5 | El build de Astro en el pipeline de release rompe el binario único | Baja | Medio | CI compila web → embed → build Go desde F4; smoke del release con panel servido |
| R6 | Staff remoto sin acceso SSH no llega al panel | Media | Medio | Recetas opt-in documentadas (Caddy+HTTPS / Tailscale) elegibles por el dueño; asunción explícita registrada en §3.3 |
| R7 | Uso por encima del techo micro (pyme) con expectativas de performance | Baja | Medio | Techo validado en CI y declarado en la documentación; índices y paginación revisadas al cruzar el techo |

### 8.2 Dependencias

| # | Dependencia | Tipo | Estado | Owner |
|---|-------------|------|--------|-------|
| D1 | Hermes runtime instalado como vecino same-host | Bloqueante | Pendiente (al iniciar F1) | Kike |
| D2 | Contrato de gateway per-sender (`X-Caller-Id` verbatim, rechazo pre-tool-call, request_contact en Telegram) | Bloqueante | Resuelta como patrón (proyecto de referencia) | Kike |
| D3 | Bot de Telegram creado y token seguro (`.env`) | Bloqueante | Pendiente (F2) | Kike |
| D4 | mcp-appointments-crm como referencia de ADRs, installer y usecases | Paralela | Resuelta | Kike |
| D5 | GitHub Releases/Actions para distribución | Paralela | Resuelta | Kike |
| D6 | Evolución upstream de #147647 (boundary nativo por plataforma) | Paralela | Pendiente (seguimiento) | Upstream Hermes |

---

## 9. Glosario y Referencias

### 9.1 Glosario

- **Hermes**: agente de IA conversacional open source que opera canales (WhatsApp, Telegram,
  e-mail) y consume las tools del MCP; la única "UI" de clientes y la interfaz conversacional
  del equipo.
- **MCP (Model Context Protocol)**: protocolo por el cual el backend expone tools a Hermes
  (Streamable HTTP POST-only, loopback).
- **X-Caller-Id**: header por request con la identidad del emisor, inyectada por el gateway
  de Hermes y resuelta contra la base; transporta identidad, nunca rol.
- **CallerResolver**: cadena `accounts → clients → desconocido` que asigna rol al caller.
- **Allowlist de tools**: restricción por plataforma de los toolsets que Hermes ofrece a las
  sesiones de canal; mitigación documentada del tool boundary del runtime.
- **Alerta pendiente**: registro en la tabla `alerts` que Hermes recoge (`get_pending_alerts`)
  y entrega por el canal del destinatario, marcándola (`mark_alert_as_sent`). El backend
  nunca envía mensajes.
- **Scheduler in-process**: goroutine del binario que computa deadlines, inactividad y reglas
  on-enter/on-exit, insertando alertas; estado en SQLite, sin servicios externos.
- **Staff agendable**: cuenta de staff con `is_bookable` y horarios, sobre la que se
  reservan turnos (unificación staff/profesional).
- **Área**: agrupación configurable de staff que concentra tickets de un tipo (round-robin,
  visibilidad, avisos).
- **Workflow declarativo**: configuración (tipos, estados, transiciones, reglas
  on-enter/on-exit, deadlines, round-robin, rol por transición) sin scripting libre.
- **Bot interno único**: el único bot de Telegram del equipo, con resolución per-emisor por
  `request_contact` + teléfono.
- **Recetas opt-in de acceso**: túnel SSH / Caddy+HTTPS / Tailscale documentadas para
  acceder al panel desde afuera; el binario es siempre loopback.
- **Techo validado**: volumen sintético (10k clientes / 100k tickets) probado en CI que
  declara hasta dónde el diseño está verificado.
- **Loopback invariant**: el MCP solo escucha `127.0.0.1:3001` y el panel solo
  `127.0.0.1:3002`; ningún puerto público por defecto.
- **DoD**: Definition of Done — criterios verificables de cierre de fase.

### 9.2 Referencias

- `docs/idea.md` — documento original de la idea (histórico del origen).
- [mcp-appointments-crm](https://github.com/egkike/mcp-appointments-crm) — proyecto de
  referencia: ADRs, `docs/gateway-contract.md` (identidad per-sender, bot de Telegram,
  personas), ADR-0018 (modelo de canales), `docs/installation.md` §"Seguridad del canal"
  (allowlist mínima de Hermes, incidente v0.10.0, issue #147647), installer y CI.
- `docs/common/prd-template.md` del proyecto de referencia — template de PRD reutilizado.
- [NousResearch/hermes-agent#147647](https://github.com/NousResearch/hermes-agent/issues/147647) —
  tool boundary del runtime (seguimiento).
- Entrevista de definición (2026-11-26): 35 decisiones registradas (Q1–Q34, Q33b, Q35),
  persistidas en la memoria de proyecto Engram.

---

## 10. Historial de Cambios

| Fecha | Versión | Autor | Cambios |
|-------|---------|-------|---------|
| 2026-11-26 | 1.0 | Kike (con el Gentleman) | Creación inicial: PRD redactado a partir de la entrevista de definición (Q1–Q35) — producto, dominio, canales, infraestructura, seguridad de Hermes, fases F1–F6. |
| 2026-11-26 | 1.1 | Kike (con el Gentleman) | Corrección de método de trabajo: eliminadas las referencias a OpenSpec/SDD (convención del proyecto de referencia); el tracking de fases pasa a features ODD (`odd/tasks/<feature>.md`) con Specs `S#` que citan textualmente los RF de este PRD. |
| 2026-11-26 | 1.2 | Kike (con el Gentleman) | Precisión de modelo de datos: `accounts.email` UNIQUE (identificador de login); `clients.email` UNIQUE nullable sobre `lower(email)` — resolución determinista del canal e-mail con degradación elegante en colisión (Q36). |
| 2026-11-26 | 1.3 | Kike (con el Gentleman) | RF22: skill de Hermes como template estático versionado, independiente de los datos del negocio (datos solo por tools MCP en runtime), con personas por rol resuelto y doble gate destructivo; instalación: docs en F1, paso opcional del installer con consent en F6 (Q37). |
| 2026-11-26 | 1.4 | Kike (con el Gentleman) | Identidad de cliente completada (revisión post-aprobación, T1a/T1b): PK UUID interno en `clients` y `accounts`; `phone` y `email` pasan a identificadores naturales UNIQUE nullable; registro por canal (e-mail-first crea con email y phone NULL — corrige el gap de identidad del canal e-mail); reclamar un email ajeno por chat = fail-closed + alerta `posible-duplicado` (Should). |
| 2026-11-26 | 1.5 | Kike (con el Gentleman) | Doctrina de atención resolutiva (revisión post-aprobación, T2a-T2d): RF23 — Hermes conduce al Cliente desde el primer mensaje (principios + ejemplos, sin libretos); `sales_url` por ítem + `online_store_url` del negocio; plantilla seed 'venta' en el motor; alerta `seguimiento-lead` post-link (Should, F5). |
| 2026-11-26 | 1.6 | Kike (con el Gentleman) | Tickets por contacto (revisión post-aprobación, T3): todo contacto con motivo genera ticket (también consultas sin compra — alimentan funnel y estadísticas); re-engagement: ticket abierto del mismo tipo retoma eventos en vez de duplicar; plantilla seed 'consulta' agregada junto a 'venta'. |
| 2026-11-26 | 1.7 | Kike (con el Gentleman) | Reservas con tres clases de cita (revisión post-aprobación, T4a-T4c): `bookings.kind` servicio/domicilio/delivery + `address` (+`clients.default_address`, `default_delivery_min`); disponibilidad por staff O área; `buffer_min` por staff agendable; RF10 extendida con 5 Gherkin. |
| 2026-11-26 | 1.8 | Kike (con el Gentleman) | Ubicaciones (revisión post-aprobación, T5a/T5b): dirección de texto + coordenadas lat/lng en clients y bookings (ubicación compartida por WhatsApp; live location nunca se almacena; asunción de fallback si el bridge no entrega el payload); ubicación del negocio en business_profile (texto + coords + `maps_url`) compartible por Hermes. |
| 2026-11-26 | 1.9 | Kike (con el Gentleman) | Datos de contacto del negocio (revisión post-aprobación, T6): `business_profile` gana `phone` (llamadas, distinto del messenger_id), `email`, `website_url` (web institucional, distinta de `online_store_url`) y `social_links` (red→URL); Hermes los comparte con conducción (RF23). |
| 2026-11-26 | 1.10 | Kike (con el Gentleman) | Canal e-mail definido (revisión post-aprobación, T7a/T7b): flujo canal-agnóstico (caller-id = correo del sender); cadencia casi inmediata (poll ~5 min configurable — el e-mail se comporta como chat lento); adjuntos out of scope v1 (evolución condicionada a multimodalidad de Hermes); pairing del canal e-mail sin verificación previa → verificar en F1 con el bridge real. |
| 2026-11-26 | 1.11 | Kike (con el Gentleman) | Conocimiento de catálogo orientado al agente (pre-T4, para que el schema nazca con los campos): `catalog_items` gana `use_cases` (qué problemas resuelve — alimenta el [RESULTADO] de RF23 y el FTS) y `limitations` (qué NO cubre — honestidad pre-venta, menos reclamos); RF4 aclara que el FTS indexa description+use_cases+limitations y anota el `get_catalog_item` opcional de detalle para T7. |
