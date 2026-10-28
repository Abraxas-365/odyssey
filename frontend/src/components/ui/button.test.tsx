import { render, screen } from '@testing-library/react'
import { MemoryRouter, Link } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { Button, ButtonLink } from './button'

describe('ButtonLink', () => {
  it('is a link that looks like a button', () => {
    render(<ButtonLink variant="outline" href="https://app.test" target="_blank" rel="noreferrer">Abrir app</ButtonLink>)
    const link = screen.getByRole('link', { name: 'Abrir app' })
    expect(link).toHaveAttribute('href', 'https://app.test')
    expect(link.className).toContain('border-border')
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('renders a router Link', () => {
    render(<MemoryRouter><ButtonLink render={<Link to="/asistente" />}>Conectar</ButtonLink></MemoryRouter>)
    expect(screen.getByRole('link', { name: 'Conectar' })).toHaveAttribute('href', '/asistente')
  })

  it('leaves Button a button', () => {
    render(<Button>Publicar</Button>)
    expect(screen.getByRole('button', { name: 'Publicar' })).toBeInTheDocument()
  })
})
