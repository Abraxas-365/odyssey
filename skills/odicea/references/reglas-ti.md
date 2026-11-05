# Reglas de TI

TI de la empresa decide qué se puede hacer con cada app. Cada app tiene un **nivel** (Sandbox, Interna o Producción) y las reglas lo miran junto con la acción. Tú no ves las reglas ni las puedes cambiar: solo ves el resultado.

## Los dos avisos

| `code` | Qué significa | Mensaje |
|---|---|---|
| `POLICY_DENIED` | Una regla de TI **no permite** esta acción. | «La regla de TI «…» no permite esto.» |
| `APPROVAL_REQUIRED` | La acción **necesita aprobación de TI** (todavía no se puede pedir desde el asistente). | «Esta acción necesita aprobación de TI (regla «…»).» |

Ejemplos de lo que traen las reglas de serie:

- Publicar una app de **Producción** necesita aprobación de TI.
- Hacer **pública** una app necesita aprobación de TI; una app de **Sandbox** no puede ser pública.
- Una app de Producción con vulnerabilidades críticas no se publica.

## Qué hacer

1. **Dile al usuario el mensaje tal cual** (trae el nombre de la regla) y en una frase qué no se hizo.
2. **No reintentes** la misma acción: dará lo mismo.
3. **No busques otra vía** para lograr lo mismo: no cambies el modo de acceso, no crees otra app, no uses otra herramienta ni un servicio externo para esquivarla. Una regla de TI no es un error técnico: es una decisión de la empresa.
4. Sugiere hablar con TI: pueden aprobarlo, cambiar el nivel de la app (por ejemplo de Sandbox a Interna) o ajustar la regla. Tú **no** puedes cambiar el nivel ni las reglas.
5. Cuando TI avise que ya está resuelto, repite la acción una vez.

## Lo que sí puedes seguir haciendo

Todo lo que las reglas no tocan sigue igual: prepara el código, crea la app, publícala en un nivel que lo permita, revisa el estado con `get_app`.
