# Reglas de TI

TI de la empresa decide qué se puede hacer con cada app. Cada app tiene un **nivel** (Sandbox, Interna o Producción) y las reglas lo miran junto con la acción. Tú no ves las reglas ni las puedes cambiar: solo ves el resultado. Una regla puede **prohibir** la acción o **pedir que TI la apruebe** antes.

## Los dos resultados

| Qué ves | Qué significa |
|---|---|
| Error `POLICY_DENIED` | Una regla de TI **no permite** esta acción. Mensaje: «La regla de TI «…» no permite esto.» |
| Resultado `pending_approval` (con `request_id` y `next_step`) | La acción **necesita aprobación de TI**: no es un error, la solicitud ya quedó abierta y **nada se hizo todavía**. |

Ejemplos de lo que traen las reglas de serie:

- Publicar una app de **Producción** necesita aprobación de TI.
- Hacer **pública** una app necesita aprobación de TI; una app de **Sandbox** no puede ser pública.
- Una app de Producción con vulnerabilidades críticas no se publica.

## Cuando TI debe aprobar (`pending_approval`)

1. Dile al usuario, en una frase, que la solicitud **quedó pendiente de TI** y que cuando la aprueben se hace sola (no hay que repetir nada).
2. **No repitas** la herramienta: abriría otra solicitud igual o no cambiaría nada.
3. Si el usuario quiere saber cómo va, `get_request` con el `request_id` (o `list_requests` para ver todas las suyas). Su `status` es `pending`, `approved`, `rejected`, `expired` o `cancelled`; `done` es verdadero cuando ya no espera.
4. **Publicar** una app de Producción es distinto: la publicación queda en `waiting_approval` («Esperando aprobación de TI») y sigue sola cuando TI aprueba. Mírala con `get_deployment`; no la vuelvas a lanzar.
5. Si la rechazan (`rejected`), muestra la nota de TI (`decision_note`) tal cual y **no lo reintentes ni busques otra vía**: que el usuario lo hable con TI. Si venció (`expired`), puedes pedirlo de nuevo si el usuario aún lo quiere. Si TI la aprobó pero no se pudo hacer, `execution_error` dice por qué.

## Cuando una regla no lo permite (`POLICY_DENIED`)

1. **Dile al usuario el mensaje tal cual** (trae el nombre de la regla) y en una frase qué no se hizo.
2. **No reintentes** la misma acción: dará lo mismo.
3. **No busques otra vía** para lograr lo mismo: no cambies el modo de acceso, no crees otra app, no uses otra herramienta ni un servicio externo para esquivarla. Una regla de TI no es un error técnico: es una decisión de la empresa.
4. Sugiere hablar con TI: pueden cambiar el nivel de la app (por ejemplo de Sandbox a Interna) o ajustar la regla. Tú **no** puedes cambiar el nivel ni las reglas, ni aprobar solicitudes.
5. Cuando TI avise que ya está resuelto, repite la acción una vez.

## Confirmar antes de lo difícil de deshacer

Antes de **volver atrás** (`rollback`) en una app de Producción o de hacer **pública** una app (`set_access`), el usuario debe decir que sí. Si la herramienta responde `CONFIRMATION_REQUIRED`, **pregúntale al usuario con las palabras del mensaje**; solo si dice que sí, repite la llamada con `confirm: true`. Nunca pongas `confirm: true` sin preguntarle.

## Lo que sí puedes seguir haciendo

Todo lo que las reglas no tocan sigue igual: prepara el código, crea la app, publícala en un nivel que lo permita, revisa el estado con `get_app`.
