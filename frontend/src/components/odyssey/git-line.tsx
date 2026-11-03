import type { ReactNode } from 'react'
import { GitBranch, GitCommitHorizontal, User } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * A commit on one line: branch · short sha · title · author. Every part is
 * optional, so a surface can drop the technical ones (branch, sha) for people
 * who only care about "what changed and who did it".
 */
export function GitLine({ branch, sha, message, author, className }: { branch?: string; sha?: string; message?: ReactNode; author?: string; className?: string }) {
  return (
    <span className={cn('flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground', className)}>
      {branch && (
        <>
          <GitBranch className="size-[13px] shrink-0" aria-hidden />
          <span className="truncate font-mono">{branch}</span>
        </>
      )}
      {sha && (
        <>
          <GitCommitHorizontal className="size-[13px] shrink-0" aria-hidden />
          <span className="font-mono">{sha.slice(0, 7)}</span>
        </>
      )}
      {message && <span className="truncate text-foreground">{message}</span>}
      {author && (
        <>
          <User className="size-[13px] shrink-0" aria-hidden />
          <span className="truncate">{author}</span>
        </>
      )}
    </span>
  )
}
