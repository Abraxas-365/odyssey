# Publish from GitHub

Connect a GitHub repository to an app and every change you push to its branch is published as a new version, with no one pressing a button. You can also publish the latest change on demand.

## Before you start

- IT must have connected GitHub for the company once. If it has not, see [When GitHub is not set up](#when-github-is-not-set-up).
- The repository must be one of those IT gave Odicea access to. Odicea lists only those.
- Your code should follow the same rules as any app (it listens on the `PORT` variable and starts by itself). See [Publish an app](publish.md#how-odicea-builds-your-code).
- Push your changes to GitHub before connecting. Odicea publishes what is in the repository, not what is on your computer.

## Connect a repository

You can connect when you create the app or later, from the app's page.

### In the console

**When you create the app.** In **Crear app** choose **Importar de GitHub** under **Cómo llega el código**. Pick the repository and the branch, then press **Crear e importar** (Create and import).

**For an existing app.** On the app's **Resumen** tab, in the **Código** (Code) panel, press **Conectar un repositorio** (Connect a repository). You can also start from **Conectar GitHub** in **Publica la primera versión**.

In the picker:

| Field | What it does |
| :--- | :--- |
| **Repositorio** (Repository) | Type part of the name in **Busca por nombre** (Search by name) and pick one. Private repositories show **Privado**. **Mostrar más** (Show more) lists further results. |
| **Rama** (Branch) | The branch to publish. The main branch is offered first, marked "(la principal)". |
| **Carpeta raíz** (Root folder) | Only with technical details on. Use it when the app lives in a subfolder of the repository. Empty means the repository root. |

Connecting publishes the latest change of the branch right away: "Conectamos tu repositorio. Lo estamos publicando…". If the repository holds several apps, Odicea offers to publish them as separate parts: see [Apps with several parts](several-parts.md).

### With your assistant

```text
Conecta el repositorio andes-logistica/gastos a la app Gastos de Odicea
y publícala desde la rama principal.
```

The assistant looks for the repository among those Odicea can see, connects it and follows the first version until it is online. It does not push code to GitHub unless you ask.

```text
Sube los cambios de esta carpeta a GitHub; la app Gastos de Odicea se publica sola.
```

## Every push publishes

After connecting, each new commit on the branch becomes a version. The **Código** panel shows the last change (branch, commit, message and author) and where it stands:

| What you see | What it means |
| :--- | :--- |
| **En cola para publicarse** | Odicea knows about the change and it is waiting its turn. |
| **Descargando tu código…** | Odicea is fetching the code. |
| **Publicado** | The change is a version of the app. |
| **Reemplazado por un cambio más nuevo** | A newer commit arrived first, so this one was skipped. |
| **No se pudo publicar** | The change did not become a version. The panel says why; with technical details on it shows more. |

Versions published this way show the channel **GitHub** in the history, with the commit and author. See [Versions and going back](versions.md). If you push to a different branch, nothing happens.

## Publish now

**Publicar ahora** (Publish now) publishes the latest change of the branch without waiting for a new push. It is in the **Código** panel and in the app's header (**Publicar ahora desde GitHub**). It needs the permission to publish versions.

**Reconstruir todo** (Rebuild everything), next to it, builds every part again even if the code did not change. Use it when IT asks, for example to pick up a new base image.

```text
Publica el último cambio de la rama de la app Gastos de Odicea.
```

## Change the branch or disconnect

To change branch, press **Desconectar** (Disconnect) in the **Código** panel and connect again choosing the other branch. Disconnecting stops publishing GitHub changes. The version that is online and the history stay as they are, and you can connect again whenever you want. Disconnecting needs the permission to change apps.

## When GitHub is not set up

If the company has not connected GitHub, the picker says "Tu organización aún no conectó GitHub." (or "GitHub todavía no está disponible en tu organización." when it is not available at all).

- If you are in IT, the notice has a **Conectar GitHub** button to the settings. Follow [Set up GitHub](it/github-app.md).
- Otherwise, ask IT to connect GitHub and come back.

If the repository you need is not in the list ("GitHub no mostró repositorios. Revisa a cuáles dio acceso TI."), IT has to give Odicea access to it on GitHub. In the meantime you can still publish from a folder or with your assistant: see [Publish an app](publish.md).

## Who may do what

| Action | Permission |
| :--- | :--- |
| Connect or disconnect a repository | `odyssey:projects:write` (Builder, TI) |
| Publish now, rebuild | `odyssey:deployments:create` (Builder, TI) |
| Set up GitHub for the company | `odyssey:integrations:manage` (TI) |

The full list is in [Permissions](../reference/permissions.md).

## Next steps

- [Versions and going back](versions.md) to see what each push produced.
- [Apps with several parts](several-parts.md) for a repository with several apps.
