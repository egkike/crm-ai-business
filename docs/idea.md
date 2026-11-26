# Proyecto Servidor MCP Independiente de Gestión de Reservas y CRM (Modelo Self-Hosted)

La idea es resolver o dar solución a una dificultad que tienen muchos negocios pequeños y medianos, en varios rubros. Se trata de la administración efectiva y eficaz de el relacionamisnto con sus Clientes y la atención de los contactos, osea un CRM. Estos pequeños negocios o consultorios generalmente no tienen los recursos para contratar una persona que le pueda administrar esa atención o contactos de sus Clientes y darle seguimiento... y tampoco tienen los recursos para comprar e implementar Sistemas complejos o muy caros. Es ahí donde apunta mi idea para tratar de resolver esa problemática... Me sigues hasta ahí?

Para ir dándole forma a los cimientos de este documento de desarrollo, van las primeras tres preguntas clave:

1. **El Canal de Interacción (La Interfaz):** ¿Cómo te imaginas que interactúan el cliente final y el dueño del negocio? ¿Será a través de una aplicación web/móvil simple, o estás pensando en algo más automatizado e inmediato como un bot de WhatsApp que agende directamente?
    La interacción con el Cliente será 100% por los siguientes canales: 1- Whatsapp, 2-E-mail y 3- Bot de Telegram (opcional y solo para Owner y Admin) y estos canales estarán manejados por el Agente AI Hermes (similar a nuestro otro proyecto https://github.com/egkike/mcp-appointments-crm el cual tomaremos como ejemplo para reutilizar todo lo que nos sea de utilidad). Hermes podrá interactuar con la base de datos por medio de un mcp y sus herramientas que desarrollaremos.

2. **El "Dolor" del Seguimiento:** Mencionaste que no tienen recursos para "darle seguimiento" a los contactos de Clientes. El olvido de los contactos por parte de los clientes es una gran pérdida de dinero para ellos. ¿Tienes pensado incluir recordatorios automáticos (por ejemplo, notificaciones o mensajes)?
    Si debemos prever notificaciones o mensajes desde Hermes hacia los Clientes y también desde Hermes hacia adentro (Owner, Admin y Staff)
    
3. **El Modelo de Negocio (Accesibilidad):** Como mencionas que no tienen presupuesto para sistemas caros, ¿cómo te imaginas que este sistema sea accesible para ellos? ¿Un modelo SaaS de suscripción mensual muy bajo, una modalidad _freemium_, o cobro por volumen de reservas exitosas?
    Este será un proyecto Open Source con licencia MIT en un repositorio de GitHub. 
    

Cuéntame cómo te imaginas estas dinámicas y seguimos profundizando.

1. En cuanto al canal está la primer disrupción que quiero hacer... No habrá páginas webs ni app movil para la interacción con Clientes. Como canal de comunicación con el sistema quiero utilizar un agente como Hermes y que hermes utilice sus canales para conectarse con mi sistema y leer/escribir las informaciones correspondientes. El sistema sería un backend hecho en Go (u otro lenguaje más conveniente si hay) con una DB en SqlLite y Hermes se conectaría con el sistema como un MCP. 
2. El Cliente que interactúe con el sistema podrá hacer consultas de sobre información del negocio y solicitar los tipos de atención tipicos de todos los negocios como preguntar sobre los productos/servicios, hacer reclamos, pedir asistencia técnica, etc. Todo esto hay que administrarlo y de eso se encargará Hermes interactuando con nuestro sistema atravez del MCP y creando Tickets por cada contacto con Clientes que ingresarán a su Workflow correspondiente dependiendo del tipo de solicitud hecha por el Cliente. También Hermes deberá generar avisos y reportes al staff, administrador o dueño encargado. Todo esto sería administrar el sistema. 
3. El modelo de negocio sería desarrollar solo la parte del sistema (Go con la db sqllite), que sería mi desarrollo, ya que Hermes es una herramienta open source que se puede descargar gratis y configurar al sistema.

Utilizar el paradigma de **MCP (Model Context Protocol)** para que un agente de IA como Hermes actúe como la interfaz de usuario (UI) natural a través de canales de chat/emails, elimina por completo la fricción de desarrollar aplicaciones móviles o webs complejas. Además, un binario en Go con SQLite es la definición perfecta de eficiencia: ultraligero, auto-contenido, facilísimo de desplegar y súper económico de mantener.


### 1. Arquitectura General y Filosofía del Sistema

El objetivo es desarrollar un sistema de gestión de contactos y fidelización de clientes (CRM) diseñado para ejecutarse de forma aislada dentro de la **VPS propia de cada cliente**.

El sistema carece de interfaces web o móviles tradicionales para los Clientes. La interacción completa con los clientes finales se delega a una instalación independiente del agente de Inteligencia Artificial **Hermes**. El sistema actúa exclusivamente como un **Servidor MCP (Model Context Protocol)** que expone herramientas (_Tools_) a Hermes a través de una conexión local segura. 

Si debemos desarrollar una Aplicación Web Interna para que el Staff, Administradores y Owner puedan interactuar con el sistema haciendo todas las tareas correspondientes a los Workflows del CRM que les corresponden como atender los Tickets abiertos para su área o rol, derivar o cerrar el ticket, etc. También tendremos una sección de reportes y estadisticas para el seguimiento y control de todos los tickets. Esto se podría considerar hacerlo con Astro + Tailwin CSS + TypeScript (FrontEnd Interno) y el Backend + mcp en Go + SQLite... Evaluemos otras alternativas con pros y contras.

- **Tecnologías del Núcleo (backend + mcp tools):** Go (Golang), SQLite (base de datos embebida).
    
- **Protocolo de Comunicación con Hermes:** MCP expuesto mediante **SSE (Server-Sent Events)** en un puerto local seguro (`http://localhost:3001`).
    
- **Modelo de Despliegue:** Proceso híbrido compuesto por **Backend en Go** en servidor Linux (VPS) y un Frontend Interno para Administración y Dashboard.
    

### 2. Arquitectura del proyecto

Quiero utilizar una arquitectura limpia (Hexagonal por ejemplo) para que sea simple de mantener y sea escalable. Veamos las mejores alternativas de arquitectura y tecnologías a utilizar. 
