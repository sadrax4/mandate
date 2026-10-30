import type { ReactNode } from 'react'
import { label, money, statusTone } from '../api'

export function Pill({ status, children }: { status: string; children?: ReactNode }) {
  return (
    <span className={`pill pill-${statusTone(status)}`}>
      {statusTone(status) === 'live' && <span className="dot" />}
      {children ?? label(status)}
    </span>
  )
}

export function Card({ title, action, children, className = '' }: { title?: ReactNode; action?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${className}`}>
      {(title || action) && (
        <header className="card-head">
          <h3>{title}</h3>
          {action}
        </header>
      )}
      {children}
    </section>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>
}

export function BudgetBar({ total, spent, reserved, currency }: { total: number; spent: number; reserved: number; currency: string }) {
  const pct = (v: number) => `${Math.min(100, Math.max(0, (v / total) * 100))}%`
  const fmt = (v: number) => money(v, currency)
  return (
    <div className="budget">
      <div className="budget-bar">
        <span className="budget-spent" style={{ width: pct(spent) }} />
        <span className="budget-reserved" style={{ width: pct(reserved) }} />
      </div>
      <div className="budget-legend">
        <span><i className="sw sw-spent" /> spent {fmt(spent)}</span>
        <span><i className="sw sw-reserved" /> reserved {fmt(reserved)}</span>
        <span className="budget-remaining">{fmt(Math.max(0, total - spent - reserved))} left of {fmt(total)}</span>
      </div>
    </div>
  )
}

export function Chip({ children, tone }: { children: ReactNode; tone?: 'good' | 'warn' }) {
  return <span className={`chip${tone ? ` ${tone}` : ''}`}>{children}</span>
}

export const navigate = (hash: string) => {
  window.location.hash = hash
}
