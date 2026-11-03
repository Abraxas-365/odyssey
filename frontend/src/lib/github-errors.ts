// Plain-Spanish text for the GitHub error codes. Mirrors internal/source/failure.go
// (the API sends the same words in `message`; the console also needs them for
// the codes GitHub's setup callback puts in `?github_error=`, which never
// pass through the API). No imports: api.ts reads this map.
export const githubMessages: Record<string, string> = {
  GITHUB_NOT_CONFIGURED: 'GitHub no está configurado en Odicea. Pide a TI que lo configure.',
  GITHUB_STATE_INVALID: 'El enlace para conectar GitHub venció o no es tuyo. Vuelve a intentarlo desde Odicea.',
  GITHUB_NOT_YOURS: 'Tu cuenta de GitHub no administra esa instalación: debe instalarla quien es dueño de la cuenta u organización en GitHub.',
  GITHUB_INSTALLATION_TAKEN: 'Esa cuenta de GitHub ya está conectada a otra organización de Odicea.',
  GITHUB_NO_ACCESS: 'Odicea ya no tiene acceso a este repositorio en GitHub. Pide a TI que revise la instalación.',
  GITHUB_BRANCH_MISSING: 'La rama conectada ya no existe en GitHub.',
  SOURCE_ROOT_MISSING: 'La carpeta elegida no existe en ese commit.',
  SOURCE_TOO_LARGE: 'El repositorio es demasiado grande para publicarlo (máximo 100 MB comprimido, 500 MB y 20.000 archivos).',
  SOURCE_IMPORT_FAILED: 'No pudimos descargar el código de GitHub. Vuelve a intentarlo.',
  GITHUB_UNAVAILABLE: 'GitHub no respondió. Lo intentaremos de nuevo; si sigue, vuelve a publicar más tarde.',
  DEPLOY_REFUSED: 'No se pudo empezar la publicación. Revisa el detalle.',
}

const internal = 'Algo falló de nuestro lado al traer tu código. Vuelve a intentarlo.'

/** The words for a GitHub code (unknown codes get the generic "our side" text). */
export function githubErrorText(code: string): string {
  return githubMessages[code] ?? internal
}
