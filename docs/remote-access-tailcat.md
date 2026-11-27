# Receta de acceso remoto: Tailcat (staff sin SSH)

> **Estado**: borrador (v1.12 del PRD). Se integra a la documentación de instalación
> completa en F6. Referencia normativa: PRD §3.3 (asunción de acceso), RF21, R6.
>
> Esta receta es **opt-in**, como todas las de acceso remoto: el binario es siempre
> loopback (panel en `127.0.0.1:3002`) y nunca abre un puerto público.

## Qué es y por qué esta receta

[Tailcat](https://github.com/tailscale/tailcat) es la herramienta open source de
Tailscale ("Tailscale sin Tailscale"): túneles punto a punto cifrados con WireGuard
en userspace, **sin cuentas, sin tailnet, sin root y sin puertos entrantes**. Es la
receta recomendada para staff interno **sin acceso SSH** al VPS: el dueño solo
comparte una dirección por un canal privado y el staff abre el panel en su
navegador. No toca tablas de rutas ni DNS del host.

El túnel es solo transporte de capa 4: el panel sigue exigiendo su propia
autenticación (login argon2id, sesión server-side, CSRF). Defense in depth intacta.

## Invariantes de seguridad de esta receta

1. **La dirección de Tailcat es un bearer credential** (embebe una pre-shared key):
   quien la conoce puede intentar conectarse. Por eso esta receta **obliga** a
   pinnear las claves públicas de los clientes con `--allow` — la dirección sola
   nunca alcanza.
2. La dirección se comparte **solo por canal privado** (nunca DNS TXT público ni
   issue/chat abierto).
3. Con clave efímera la dirección muere al reiniciar; para el panel se usa una
   clave guardada con región fija (dirección estable entre reinicios).

## Paso 1 — Staff: generar identidad (una vez)

En la máquina de cada persona del equipo:

```sh
tailcat genkey --client --key=default
# imprime: nodekey:cfb6bf...ddfd16
```

La persona envía esa clave pública (`nodekey:...`) al dueño por un canal privado.
Es un valor público: no habilita nada por sí solo hasta que el dueño la pinee.

## Paso 2 — VPS (dueño): clave estable del panel + pinneo de clientes

```sh
# Clave guardada con región DERP fija → dirección estable entre reinicios:
tailcat genkey --key=crm-panel --fixed-region
# imprime la dirección tcXXXXX...

# Servir SOLO el puerto del panel, solo a clientes pinnados:
tailcat serve --allow=nodekey:<pubkey-staff-1>,nodekey:<pubkey-staff-2> 3002
# 🐈 Server listening with saved key "crm-panel": tcXXXXXXXXX
```

El dueño comparte la dirección `tcXXXXXXXXX` a cada staff pinnado por canal privado.
Este proceso puede correr como servicio user-level (systemd/launchd) igual que el
binario del CRM.

## Paso 3 — Staff: acceder al panel

```sh
tailcat forward tcXXXXXXXXX 3002   # → http://127.0.0.1:3002
# o directamente:
tailcat browse tcXXXXXXXXX          # abre el navegador en el puerto local
```

Login del panel como siempre: el túnel no reemplaza ninguna autenticación.

## Opción cero terceros: DERP propio

Por defecto el arranque de la conexión usa los relays DERP públicos de Tailscale
(gratuitos, rate-limited, sin SLA — el tráfico sigue siendo E2E WireGuard, el relay
no puede leerlo). Para eliminar toda dependencia de terceros, el dueño puede correr
su propio relay con
[`derper`](https://github.com/tailscale/tailscale/tree/main/cmd/derper#derper) y
generar la clave del servidor apuntándolo:

```sh
tailcat genkey --key=crm-panel --region=derp.tu-dominio.example
```

La dirección embebe el hostname del relay propio; los clientes no necesitan flags
extra ni contactan los relays de Tailscale.

## Revocación

- **Un staff se va**: quitar su `nodekey:` de la lista `--allow` y reiniciar el
  proceso. Su handshake queda ignorado aunque conozca la dirección.
- **Compromiso de la dirección**: `tailcat genkey --delete --key=crm-panel`, generar
  clave nueva y re-compartir la dirección nueva por canal privado.

## Nota operativa (equipo de desarrollo)

Tailcat también se usa como acceso de **pruebas en la VM server** de desarrollo:
misma receta, claves de prueba, sin exponer puertos de la VM.
