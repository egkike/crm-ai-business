# Pairing — conectar Hermes al servidor MCP

> Esta es la guía del **operador** (quien instala). Hermes es el agente; este servidor no
> es un chatbot: expone herramientas MCP y el gateway de Hermes decide cuándo llamarlas.

## 1. El contrato de identidad

Cada request que llega a `/mcp` trae el header **`X-Caller-Id`**: la identidad de quien
escribió, inyectada por el gateway de Hermes.

- **Se envía verbatim**: sin recortes, sin normalizar, sin cachear entre emisores.
- **El rol no viaja en el header.** Se resuelve en la base: `accounts` (owner, admin,
  staff) primero, luego `clients`. Sin fila → `-32000` «no te reconozco».
- Un header ausente se rechaza antes de ejecutar nada: `-32000` «no se proporcionó X-Caller-Id».

El servidor escucha **solo** en `127.0.0.1:3001` (loopback). El gateway de Hermes corre en
el mismo host; el canal nunca expone el puerto.

## 2. Canal de clientes por WhatsApp — verificado

Es el camino estándar: el gateway lee el `from` de cada mensaje y lo usa como `X-Caller-Id`.

1. Pareá el canal de WhatsApp de Hermes contra el número del negocio.
2. Verificá el envío de identidad: un número conocido debe resolver su rol; un número
   nuevo debe recibir `-32000`.
3. Verificá el loop de registro: el número nuevo pasa por `register_client` y el
   reintento devuelve datos (el historial de este contrato está en el proyecto
   predecesor y su CI).

## 3. Canal de clientes por e-mail — **pendiente de verificación**

Este canal **no tiene verificación previa** en el proyecto de referencia (era
WhatsApp-céntrico). Antes de darlo por operativo, comprobá:

- [ ] El gateway pollea la casilla cada N minutos (default sugerido: 5) y entrega el
      mensaje al agente.
- [ ] El `X-Caller-Id` es la **dirección del remitente** (normalizada a minúsculas por el
      servidor al resolver, no por el gateway).
- [ ] Un remitente desconocido recibe `-32000` y completa `register_client` con su correo
      (queda creado con `email` y sin teléfono).
- [ ] Un segundo correo del mismo remitente resuelve al mismo cliente (no duplica).
- [ ] Los adjuntos se ignoran en v1 (sin imágenes ni PDFs): el texto conduce.
- [ ] El reintento del loop ocurre **una sola vez**.

Si alguna casilla falla, el producto sigue operativo por WhatsApp: el canal de e-mail es
un agregado, no un requisito para el flujo principal.

## 4. Bot interno del equipo por Telegram

Un **único bot** para staff, admin y dueño.

1. Cada persona abre el bot y comparte su contacto **una vez** (`request_contact`); ese
   teléfono es su identidad y resuelve contra `accounts`.
2. Un teléfono que no corresponde a ninguna cuenta se rechaza: los internos no se
   autoregistran (las cuentas las crea el dueño o el admin).
3. Verificá: staff ve sus tickets y los del equipo interno; un cliente en este bot no
   existe — es otro canal.

## 5. La skill del agente

Instalá `skills/recepcionista-negocio/SKILL.md` en el directorio de skills de Hermes
(`~/.hermes/skills/recepcionista-negocio/`). Es un template **estático**: no contiene datos
del negocio y no se regenera cuando el negocio cambia.

> La skill es texto en reposo: enuncia comportamiento, no otorga permisos.

## 6. La allowlist de herramientas — obligatoria

El gateway de Hermes arranca con acceso amplio por defecto. Antes de dar el canal a
clientes, restringí las herramientas por plataforma a lo indispensable:
el MCP de este negocio + `skills`, `clarify`, `memory`, `todo`.

**Todo lo demás se deshabilita**, incluido `terminal`, `file`, `execute_code`, `browser`,
`computer_use` y los plugins que no uses: cualquiera de ellos puede escribir la base sin
pasar por las herramientas del negocio.

- Auditá con la lista de herramientas de la plataforma antes y después de cada upgrade
  del runtime o de volver a parear el canal.
- Probá que el límite funciona: pedile al bot que ejecute un comando de terminal (debe
  responder que no tiene esa herramienta) y después pedile el catálogo (debe responder
  con datos del negocio).

El procedimiento completo y su verificación viven en la documentación de instalación
(RF20); esta sección es el recordatorio operativo.

## 7. Checklist mínimo antes de dar el canal a clientes

- [ ] `/healthz` responde 200 y `GET /mcp` responde 405 (el endpoint es POST-only).
- [ ] Un número conocido resuelve su rol; uno desconocido recibe `-32000`.
- [ ] El loop de registro funciona y no duplica clientes.
- [ ] La skill está instalada y el agente se presenta como asistente del negocio.
- [ ] La allowlist está aplicada y la prueba de terminal falla.
- [ ] El staff ve solo lo que su rol permite en el bot interno.
