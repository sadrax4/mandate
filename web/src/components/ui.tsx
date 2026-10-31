import type { ReactNode, SelectHTMLAttributes, MouseEvent } from 'react'
import { label, money, statusTone } from '../api'

export function Pill({ status, children }: { status: string; children?: ReactNode }) {
  return (
    <span className={`pill pill-${statusTone(status)}`}>
      {statusTone(status) === 'live' && <span className="dot" />}
      {children ?? label(status)}
    </span>
  )
}

export function Card({ title, action, children, className = '', id, tone, onClick }: {
  title?: ReactNode
  action?: ReactNode
  children: ReactNode
  className?: string
  id?: string
  tone?: 'attention' | 'decision'
  onClick?: (e: MouseEvent<HTMLElement>) => void
}) {
  return (
    <section className={`card ${tone ?? ''} ${className}`} id={id} onClick={onClick}>
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

export function BudgetBar({ total, spent, reserved, currency, compact }: { total: number; spent: number; reserved: number; currency: string; compact?: boolean }) {
  const pct = (v: number) => `${Math.min(100, Math.max(0, (v / total) * 100))}%`
  const fmt = (v: number) => money(v, currency)
  return (
    <div className="budget">
      <div className={`budget-bar${compact ? ' compact' : ''}`} role="img" aria-label={`${fmt(spent)} spent, ${fmt(reserved)} reserved of ${fmt(total)}`}>
        <span className="budget-spent" style={{ width: pct(spent) }} />
        <span className="budget-reserved" style={{ width: pct(reserved) }} />
      </div>
      {!compact && (
        <div className="budget-legend">
          <span><i className="sw sw-spent" /> spent {fmt(spent)}</span>
          <span><i className="sw sw-reserved" /> reserved {fmt(reserved)}</span>
          <span className="budget-remaining num">{fmt(Math.max(0, total - spent - reserved))} left of {fmt(total)}</span>
        </div>
      )}
    </div>
  )
}

export function Chip({ children, tone }: { children: ReactNode; tone?: 'good' }) {
  return <span className={`chip${tone ? ` ${tone}` : ''}`}>{children}</span>
}

/** A row of figures: value on top, label below. */
export function Statline({ items, compact }: { items: { value: ReactNode; label: string }[]; compact?: boolean }) {
  return (
    <div className={`statline${compact ? ' compact' : ''}`}>
      {items.map((it) => (
        <div key={it.label}>
          <b className="num">{it.value}</b>
          <span>{it.label}</span>
        </div>
      ))}
    </div>
  )
}

export type TrackerStep = { key: string; label: string; state: 'done' | 'current' | 'future' | 'failed' | 'revised'; note?: string }

/** Steps of a story as dots on a line: done, current (pulsing), future, failed, revised. */
export function Tracker({ steps, size }: { steps: TrackerStep[]; size?: 'sm' }) {
  return (
    <div className={`tracker${size === 'sm' ? ' sm' : ''}`} aria-label={steps.map((s) => `${s.label} (${s.state})`).join(' → ')}>
      {steps.map((s) => (
        <span key={s.key} className={`step ${s.state}`}>
          <span className="dot" />
          {s.label}
          {s.note && <span className="note">{s.note}</span>}
        </span>
      ))}
    </div>
  )
}

/** A select that reads like a link inside a sentence. */
export function InlineSelect(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={`select-inline ${props.className ?? ''}`} />
}

export function SuggestionChip({ text, tag, title, onClick }: { text: string; tag?: string; title?: string; onClick: () => void }) {
  return (
    <button type="button" className="sugg" title={title} onClick={onClick}>
      {text}
      {tag && <span className="tag">{tag}</span>}
    </button>
  )
}

export const navigate = (hash: string) => {
  window.location.hash = hash
}
