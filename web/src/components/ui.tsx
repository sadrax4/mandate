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

export function Card({ title, action, children, className = '', id, tone, state, onClick }: {
  title?: ReactNode
  action?: ReactNode
  children: ReactNode
  className?: string
  id?: string
  tone?: 'attention' | 'decision'
  /** Colours the decision card's head band: good (paid), warn (your turn), neutral (no result), live (working). */
  state?: string
  onClick?: (e: MouseEvent<HTMLElement>) => void
}) {
  return (
    <section className={`card ${tone ?? ''} ${className}`} id={id} data-state={state} onClick={onClick}>
      {(title || action) && (
        <header className="card-head">
          <h2 className="card-title">{title}</h2>
          {action}
        </header>
      )}
      {children}
    </section>
  )
}

/** A decorative glyph that screen readers skip. */
export function Glyph({ children }: { children: ReactNode }) {
  return <span aria-hidden="true">{children}</span>
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

export type TrackerStep = {
  key: string
  label: string
  state: 'done' | 'current' | 'future' | 'failed' | 'revised'
  note?: string
  /** Decoration before the label (a step number); screen readers skip it. */
  marker?: string
  /** The current step is waiting on the human, not on the agent: drawn in the "your turn" colour. */
  yours?: boolean
}

// aria-current already says "current step"; the words fill in the other states.
const STEP_WORD: Record<TrackerStep['state'], string> = { done: 'done', current: '', future: '', failed: 'stopped here', revised: 'revised' }

/** Steps of a story as dots on a line: done, current (pulsing), future, failed, revised. */
export function Tracker({ steps, size, label: name }: { steps: TrackerStep[]; size?: 'sm' | 'full'; label?: string }) {
  return (
    <ol className={`tracker${size ? ' ' + size : ''}`} role="list" aria-label={name ?? 'Progress'}>
      {steps.map((s) => (
        <li key={s.key} className={`step ${s.state}${s.yours ? ' yours' : ''}`} aria-current={s.state === 'current' ? 'step' : undefined}>
          <span className="dot" aria-hidden="true" />
          {s.marker && <span className="marker" aria-hidden="true">{s.marker}</span>}
          {s.label}
          {STEP_WORD[s.state] && <span className="sr-only">, {STEP_WORD[s.state]}</span>}
          {s.note && <span className="note"><span className="sr-only">, </span>{s.note}</span>}
        </li>
      ))}
    </ol>
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
