import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { api, money, type Mandate, type Mission } from '../api'
import { useConfig } from '../config'
import { describeRules, relativeTime, remainingOf, shortDate } from '../format'
import { BudgetBar, Card, Chip, Empty, InlineSelect, Pill, Statline, SuggestionChip, Tracker, navigate, type TrackerStep } from '../components/ui'

const DEMO_GOALS: { goal: string; note?: string; tag?: string; kind?: string }[] = [
  { goal: 'A mechanical keyboard with brown switches and a USB-C hub with HDMI, both under $120' },
  { goal: 'Noise cancelling headphones for open-office work' },
  { goal: 'A large felt desk mat under $40' },
  { goal: 'A gooseneck kettle and a coffee grinder for pour-over', tag: 'outside the mandate', note: "kitchen gear, outside the demo mandate's categories: watch the agent explain" },
  { goal: 'A two-hour product photo shoot for 20 items', tag: 'agents negotiate', kind: 'negotiate', note: 'two agents negotiate the price, you approve, PayPal settles' },
]

const REPEAT_OPTIONS = [
  { days: 0, label: 'Run once' },
  { days: 7, label: 'Repeat weekly' },
  { days: 30, label: 'Repeat monthly' },
  { days: 90, label: 'Repeat quarterly' },
]

// A capture PayPal is still settling is PayPal's turn, not yours.
const capturedAtPayPal = (s?: string) => s === 'PENDING' || s === 'COMPLETED' || s === 'PARTIALLY_REFUNDED'
/** Missions waiting on the human: a cart to approve, or a checkout to finish. One rule for the band and the cards. */
const needsYou = (ms: Mission) => ms.status === 'awaiting_approval' || (ms.status === 'awaiting_payment' && !capturedAtPayPal(ms.payment_status))

const VISIBLE_MANDATES = 6
const VISIBLE_APPROVALS = 5
const VISIBLE_MISSIONS = 8

const readFlag = (key: string) => {
  try {
    return localStorage.getItem(key) === '1'
  } catch {
    return false
  }
}
const writeFlag = (key: string, on: boolean) => {
  try {
    localStorage.setItem(key, on ? '1' : '0')
  } catch {
    /* private mode */
  }
}

export default function Dashboard() {
  const [mandates, setMandates] = useState<Mandate[]>([])
  const [missions, setMissions] = useState<Mission[]>([])
  const [loadError, setLoadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [busy, setBusy] = useState('')
  const [showAll, setShowAll] = useState(false)
  const [approvalsAll, setApprovalsAll] = useState(false)
  const [missionsAll, setMissionsAll] = useState(false)
  const [selectedId, setSelectedId] = useState('')
  const [guideOpen, setGuideOpen] = useState(() => readFlag('mandate.guide.open'))
  const config = useConfig()

  // loadError comes and goes with the 4-second poll; actionError stays until the next action.
  const load = () =>
    Promise.all([api.mandates(), api.missions()])
      .then(([m, ms]) => {
        setMandates(m)
        setMissions(ms)
        setLoadError('')
      })
      .catch((e) => setLoadError(e.message))

  useEffect(() => {
    load()
    const t = setInterval(load, 4000)
    return () => clearInterval(t)
  }, [])

  const sorted = useMemo(() => [...mandates].sort((a, b) => (a.created_at < b.created_at ? 1 : -1)), [mandates])
  const demo = mandates.find((m) => m.demo)

  // The goal bar works under one mandate: the demo mandate, else the newest.
  useEffect(() => {
    if (sorted.length === 0) return
    if (selectedId && sorted.some((m) => m.id === selectedId)) return
    setSelectedId((demo ?? sorted[0]).id)
  }, [sorted, demo, selectedId])

  // Reveal a mandate card: expand the list only if the card is hidden, then scroll to it and flash it once it is in the DOM.
  const [revealId, setRevealId] = useState('')
  const reveal = (id: string) => {
    if (sorted.findIndex((m) => m.id === id) >= VISIBLE_MANDATES) setShowAll(true)
    setRevealId(id)
  }
  useEffect(() => {
    if (!revealId) return
    const el = document.getElementById('mandate-' + revealId)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block: 'start' })
    el.classList.add('flash')
    const t = setTimeout(() => el.classList.remove('flash'), 1800)
    setRevealId('')
    return () => clearTimeout(t)
  }, [revealId, showAll, mandates])

  const createDemo = async () => {
    setBusy('demo')
    setActionError('')
    try {
      const r = await api.demoMandate()
      await load()
      setSelectedId(r.mandate.id)
      if (!r.created) reveal(r.mandate.id)
    } catch (e: any) {
      setActionError(e.message)
    } finally {
      setBusy('')
    }
  }

  const select = (id: string) => {
    setSelectedId(id)
    const input = document.getElementById('goal-input') as HTMLInputElement | null
    if (input) {
      const r = input.getBoundingClientRect()
      if (r.top < 0 || r.bottom > window.innerHeight) input.scrollIntoView({ behavior: 'smooth', block: 'center' })
      input.focus({ preventScroll: true })
    }
  }

  const toggleGuide = () => {
    setGuideOpen((v) => {
      writeFlag('mandate.guide.open', !v)
      return !v
    })
  }

  const steps: TrackerStep[] = useMemo(() => {
    const done = [mandates.length > 0, missions.length > 0, missions.some((m) => m.status === 'paid')]
    const labels = ['Mandate', 'Mission', 'Approve & pay']
    const markers = ['①', '②', '③']
    const current = done.findIndex((d) => !d)
    return labels.map((label, i) => ({ key: label, label, marker: markers[i], state: done[i] ? 'done' : i === current ? 'current' : 'future' }))
  }, [mandates.length, missions])

  // Your turn: carts to approve, and approved carts whose PayPal checkout is unfinished (first: they hold budget).
  const awaiting = missions
    .filter(needsYou)
    .sort((a, b) => (a.status === b.status ? 0 : a.status === 'awaiting_payment' ? -1 : 1))
  const paying = awaiting.some((ms) => ms.status === 'awaiting_payment')
  const standing = missions.filter((ms) => (ms.repeat_days ?? 0) > 0)
  const recent = useMemo(() => [...missions].sort((a, b) => (a.updated_at < b.updated_at ? 1 : -1)), [missions])
  const mandateName = (id: string) => mandates.find((m) => m.id === id)?.name ?? '—'
  const visibleMandates = showAll ? sorted : sorted.slice(0, VISIBLE_MANDATES)
  const cur = config?.currency || mandates[0]?.currency || 'USD'
  const sum = (f: (m: Mandate) => number) => mandates.reduce((s, m) => s + f(m), 0)

  return (
    <div className="page">
      {mandates.length === 0 ? (
        <section className="hero empty-hero">
          <h1 className="t-display">Give an AI agent a budget. Keep the final say.</h1>
          <p className="hero-sub">A mandate is the authority you grant: a budget, a per-item cap, allowed categories, blocked merchants. The agent works inside it; you approve; PayPal pays.</p>
          <div className="actions">
            <button className="btn primary lg" onClick={createDemo} disabled={busy === 'demo'}>{busy === 'demo' ? 'Creating…' : 'Create demo mandate'}</button>
            <a className="btn ghost lg" href="#/mandates/new">New mandate</a>
          </div>
          <div className="hero-steps">
            <Tracker steps={steps} size="sm" label="Getting started" />
          </div>
          <HowItWorks />
        </section>
      ) : (
        <GoalBar
          mandates={sorted}
          selectedId={selectedId}
          onSelect={setSelectedId}
          demoExists={!!demo}
          busy={busy}
          onDemo={createDemo}
          onStarted={load}
          steps={steps}
          guideOpen={guideOpen}
          onToggleGuide={toggleGuide}
        />
      )}
      {actionError && <div className="alert bad" role="alert">{actionError}</div>}
      {loadError && <div className="alert bad" role="status">Could not load the dashboard: {loadError}</div>}

      {awaiting.length > 0 && (
        <Card tone="attention" title={paying ? 'Waiting on you' : 'Needs your approval'} action={<span className="pill pill-warn num">{awaiting.length}</span>} className="band">
          <div className="rows" role="list">
            {(approvalsAll ? awaiting : awaiting.slice(0, VISIBLE_APPROVALS)).map((ms) => {
              const stale = ms.status === 'awaiting_approval' && ms.fits === false
              const act = ms.status === 'awaiting_payment' ? 'Finish payment' : stale ? 'Review' : 'Review & approve'
              return (
                <div key={ms.id} className="row approval clickable" role="listitem" onClick={() => navigate(`#/missions/${ms.id}`)}>
                  <span className="goal-cell">
                    <span className="goal" title={ms.goal}>{ms.goal}</span>
                    {stale ? (
                      <span className="what over">No longer fits: the budget moved since the agent proposed it</span>
                    ) : (
                      ms.proposal_first && !ms.proposal_first.toLowerCase().startsWith(ms.goal.toLowerCase().slice(0, 24)) && <span className="what" title={ms.proposal_first}>{ms.proposal_first}{(ms.proposal_items ?? 0) > 1 ? ` +${(ms.proposal_items ?? 0) - 1} more` : ''}</span>
                    )}
                  </span>
                  <span className="sub">
                    <span className={`amount num${stale ? ' over' : ''}`} title={stale ? 'The budget moved: this cart no longer fits the mandate' : undefined}>{ms.proposal_total ? money(ms.proposal_total, ms.proposal_currency) : ''}</span>
                    <span className="muted">{mandateName(ms.mandate_id)}</span>
                    <span className="when num" title={new Date(ms.updated_at).toLocaleString()}>{relativeTime(ms.updated_at)}</span>
                  </span>
                  <a className={`btn ${stale ? '' : 'primary '}sm`} href={`#/missions/${ms.id}`} aria-label={`${act}: ${ms.goal}`} onClick={(e) => e.stopPropagation()}>{act}</a>
                </div>
              )
            })}
          </div>
          {!approvalsAll && awaiting.length > VISIBLE_APPROVALS && (
            <div className="rows-more"><button type="button" className="link" onClick={() => setApprovalsAll(true)}>and {awaiting.length - VISIBLE_APPROVALS} more…</button></div>
          )}
        </Card>
      )}

      {mandates.length > 0 && (
        <section className="band">
          <div className="section-head">
            <h2 className="section">Your mandates<span className="count num">{mandates.length}</span></h2>
            <Statline
              compact
              items={[
                { value: money(sum((m) => m.total_budget), cur), label: 'Total budget' },
                { value: money(sum((m) => m.spent), cur), label: 'Spent' },
                { value: money(sum((m) => m.reserved), cur), label: 'Reserved' },
                { value: money(sum(remainingOf), cur), label: 'Available' },
              ]}
            />
          </div>
          <div className={`mandate-grid${visibleMandates.length === 1 ? ' single' : ''}`}>
            {visibleMandates.map((m) => (
              <MandateCard key={m.id} mandate={m} missions={missions.filter((x) => x.mandate_id === m.id)} selected={m.id === selectedId} onSelect={() => select(m.id)} onChanged={load} />
            ))}
            <a className="mandate-new" href="#/mandates/new">
              <span className="plus" aria-hidden="true">+</span>
              <span>New mandate</span>
              <span className="muted secondary">a budget, limits and rules for the agent</span>
            </a>
          </div>
          {sorted.length > VISIBLE_MANDATES && (
            <div className="actions" style={{ justifyContent: 'center', marginTop: 10 }}>
              <button className="btn ghost" onClick={() => setShowAll((v) => !v)}>
                {showAll ? `Show the ${VISIBLE_MANDATES} newest mandates` : `Show all ${sorted.length} mandates`}
              </button>
            </div>
          )}
        </section>
      )}

      {standing.length > 0 && (
        <Card title="Standing orders" className="band" action={<span className="muted subtitle">re-run on a schedule, always inside the mandate</span>}>
          <div className="rows" role="list">
            {standing.map((ms) => {
              const last = ms.last_run_id ? missions.find((x) => x.id === ms.last_run_id) : ms
              return (
                <div key={ms.id} className="row standing" role="listitem">
                  <span className="muted so-icon" aria-hidden="true">⟳</span>
                  <span className="goal"><a href={`#/missions/${ms.id}`}>{ms.goal}</a></span>
                  <span className="sub">
                    <span className="muted">{mandateName(ms.mandate_id)}</span>
                    <span className="muted num">every {ms.repeat_days} days · next {shortDate(ms.next_run_at)}</span>
                    <span className="last">{last ? <><span className="muted">last run</span> <a href={`#/missions/${last.id}`}><Pill status={last.status} /></a></> : '—'}</span>
                  </span>
                  <StandingOrderButtons mission={ms} onChanged={load} />
                </div>
              )
            })}
          </div>
        </Card>
      )}

      {(mandates.length > 0 || missions.length > 0) && (
        <Card title="Recent missions" className="band">
          {missions.length === 0 ? (
            <Empty>No missions yet. Start one above.</Empty>
          ) : (
            <>
              <div className="rows" role="list">
                {(missionsAll ? recent : recent.slice(0, VISIBLE_MISSIONS)).map((ms) => (
                  <div key={ms.id} className="row recent clickable" role="listitem" onClick={() => navigate(`#/missions/${ms.id}`)}>
                    <span className="goal" title={ms.goal}><a href={`#/missions/${ms.id}`} onClick={(e) => e.stopPropagation()}>{ms.goal}</a></span>
                    <span className="sub">
                      <span className="status-cell"><Pill status={ms.status} /></span>
                      <span className="amount num">{ms.proposal_total && ['paid', 'awaiting_approval', 'awaiting_payment'].includes(ms.status) ? money(ms.proposal_total, ms.proposal_currency) : ''}</span>
                      <span className="muted">{mandateName(ms.mandate_id)}</span>
                      <span className="when num" title={new Date(ms.updated_at).toLocaleString()}>{relativeTime(ms.updated_at)}</span>
                    </span>
                  </div>
                ))}
              </div>
              {missions.length > VISIBLE_MISSIONS && (
                <div className="rows-more"><button type="button" className="link" onClick={() => setMissionsAll((v) => !v)}>{missionsAll ? `Show the ${VISIBLE_MISSIONS} newest` : `Show all ${missions.length}`}</button></div>
              )}
            </>
          )}
        </Card>
      )}
    </div>
  )
}

function GoalBar({ mandates, selectedId, onSelect, demoExists, busy, onDemo, onStarted, steps, guideOpen, onToggleGuide }: {
  mandates: Mandate[]
  selectedId: string
  onSelect: (id: string) => void
  demoExists: boolean
  busy: string
  onDemo: () => void
  onStarted: () => void
  steps: TrackerStep[]
  guideOpen: boolean
  onToggleGuide: () => void
}) {
  const [goal, setGoal] = useState('')
  const [kind, setKind] = useState('buy')
  const [repeat, setRepeat] = useState(0)
  const [starting, setStarting] = useState(false)
  const [startErr, setStartErr] = useState('')
  const start = async (e: FormEvent) => {
    e.preventDefault()
    if (!goal.trim() || !selectedId) return
    setStarting(true)
    setStartErr('')
    try {
      const ms = await api.createMission(selectedId, goal.trim(), repeat, kind)
      onStarted()
      navigate(`#/missions/${ms.id}`)
    } catch (e: any) {
      setStartErr(e.message)
    } finally {
      setStarting(false)
    }
  }
  const pick = (g: (typeof DEMO_GOALS)[number]) => {
    setGoal(g.goal)
    setKind(g.kind ?? 'buy')
    document.getElementById('goal-input')?.focus()
  }
  return (
    <section className="hero">
      <div className="hero-head">
        <div className="hero-title">
          <h1 className="t-display">What should the agent buy?</h1>
          <p className="hero-sub">Give it a goal under a mandate. It researches, compares and proposes. You approve. PayPal pays.</p>
        </div>
        <div className="actions">
          <button className="btn ghost" onClick={onDemo} disabled={busy === 'demo'}>{busy === 'demo' ? 'Creating…' : demoExists ? 'Show demo mandate' : 'Create demo mandate'}</button>
          <a className="btn" href="#/mandates/new">New mandate</a>
        </div>
      </div>
      <form className="goal-bar" onSubmit={start}>
        <input
          id="goal-input"
          className="goal-input"
          value={goal}
          onChange={(e) => setGoal(e.target.value)}
          placeholder={kind === 'negotiate' ? 'e.g. logo design, three concepts' : 'e.g. a 4K USB-C monitor under $300'}
          aria-label="Goal for the agent"
        />
        <button className="btn primary goal-go" disabled={starting || !goal.trim()}>{starting ? 'Starting…' : 'Start mission'}</button>
      </form>
      {startErr && <div className="alert bad" role="alert">{startErr}</div>}
      <div className="goal-settings">
        <span className="setting">
          <span className="setting-label">Using</span>
          <InlineSelect value={selectedId} onChange={(e) => onSelect(e.target.value)} aria-label="Mandate for the next mission" title="The mandate the agent works under">
            {mandates.map((m) => (
              <option key={m.id} value={m.id}>{m.name} · {money(remainingOf(m), m.currency)} left</option>
            ))}
          </InlineSelect>
        </span>
        <span className="sep">·</span>
        <span className="setting">
          <InlineSelect value={kind} onChange={(e) => setKind(e.target.value)} aria-label="Mission kind" title="Buy: research a catalog and propose a cart. Negotiate: a buyer agent haggles with a seller agent for a service; the agreed price becomes the proposal.">
            <option value="buy">Buy a product</option>
            <option value="negotiate">Hire a service</option>
          </InlineSelect>
        </span>
        <span className="sep">·</span>
        <span className="setting">
          <InlineSelect value={repeat} onChange={(e) => setRepeat(Number(e.target.value))} aria-label="Repeat schedule" title="Standing order: run this goal again on a schedule, inside the same mandate">
            {REPEAT_OPTIONS.map((o) => <option key={o.days} value={o.days}>{o.label}</option>)}
          </InlineSelect>
        </span>
      </div>
      <div className="hero-try">
        <span className="try-label">Try:</span>
        {DEMO_GOALS.map((g) => <SuggestionChip key={g.goal} text={g.goal} tag={g.tag} title={g.note} onClick={() => pick(g)} />)}
      </div>
      <div className="hero-steps">
        <Tracker steps={steps} size="sm" label="Getting started" />
        <button type="button" className="link" aria-expanded={guideOpen} onClick={onToggleGuide}>{guideOpen ? 'Hide how it works' : 'How it works'}</button>
      </div>
      {guideOpen && <HowItWorks />}
    </section>
  )
}

function HowItWorks() {
  const config = useConfig()
  const mock = config?.paypal_mode === 'mock'
  return (
    <div className="card howitworks">
      <div className="cols">
        <div className="col"><span className="n">1</span><span>Create the demo mandate, or set your own limits with New mandate.</span></div>
        <div className="col"><span className="n">2</span><span>Pick a suggested goal and Start mission. The agent searches {config?.catalog === 'channel3' ? 'real products through Channel3' : 'the demo catalog'}, checks the mandate and proposes.</span></div>
        <div className="col"><span className="n">3</span><span>Approve &amp; pay with PayPal{mock ? ' (a built-in simulator stands in here)' : config?.demo_buyer_hint ? <> and log in with the sandbox buyer <code>{config.demo_buyer_hint}</code></> : ''}. The payment shows up in the Ledger and the Insights charts.</span></div>
      </div>
      <p className="then">Then try: approve a second cart with one click, reject with feedback, start a goal the mandate cannot fit and accept the agent's fix, or make a mission a monthly standing order. Nothing here moves real money: PayPal runs in {mock ? 'simulated' : 'sandbox'} mode.</p>
    </div>
  )
}

function MandateCard({ mandate: m, missions, selected, onSelect, onChanged }: { mandate: Mandate; missions: Mission[]; selected: boolean; onSelect: () => void; onChanged: () => void }) {
  const [open, setOpen] = useState(false)
  const waiting = missions.filter(needsYou).length
  const working = missions.filter((x) => x.status === 'running' || (x.status === 'awaiting_payment' && !needsYou(x))).length
  const remaining = remainingOf(m)
  const mini = [...missions].sort((a, b) => (a.created_at < b.created_at ? 1 : -1)).slice(0, 5)
  return (
    <Card
      id={'mandate-' + m.id}
      className={`mandate-card${selected ? ' selected' : ''}${open ? ' open' : ''}`}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('a, button, details, summary, input')) return
        onSelect()
      }}
    >
      <div className="name-row">
        <h3 className="name" title={m.name}>{m.name}</h3>
        <span className="name-pills">
          {waiting > 0 && <Pill status="awaiting_approval">{waiting} need{waiting === 1 ? 's' : ''} you</Pill>}
          {working > 0 && <Pill status="running">{working} working</Pill>}
        </span>
      </div>
      <div className="money-row">
        <span className="num num-lg">{money(remaining, m.currency)}</span>
        <span className="of num">left of {money(m.total_budget, m.currency)}{m.reserved > 0 && <span className="reserved"> · {money(m.reserved, m.currency)} reserved</span>}</span>
      </div>
      <BudgetBar compact total={m.total_budget} spent={m.spent} reserved={m.reserved} currency={m.currency} />
      <div className="rules" title={describeRules(m)}>{describeRules(m)}</div>
      <div className="chips">
        {m.auto_approve_under > 0 && !m.require_approval && <Chip>auto-pay under {money(m.auto_approve_under, m.currency)}</Chip>}
        {m.has_saved_payment && <Chip tone="good">PayPal saved</Chip>}
      </div>
      <div className="card-foot">
        <button type="button" className="link use" aria-pressed={selected} aria-label={`${selected ? 'Selected for the next mission' : 'Use for next mission'}: ${m.name}`} onClick={onSelect}>{selected ? 'Selected for the next mission ✓' : 'Use for next mission'}</button>
        <button type="button" className="link" aria-expanded={open} aria-label={`Details: ${m.name}`} onClick={() => setOpen((v) => !v)}>Details <span aria-hidden="true">{open ? '▴' : '▾'}</span></button>
      </div>
      {open && (
        <div className="card-details">
          <div className="chips" style={{ margin: 0 }}>
            {!(m.auto_approve_under > 0 && !m.require_approval) && <Chip>every cart needs your approval</Chip>}
            {m.has_saved_payment ? (
              <>
                <Chip tone="good">PayPal saved{m.vault_payer ? ` · ${m.vault_payer}` : ''} · one-click approvals</Chip>
                <ForgetButton mandate={m} onChanged={onChanged} />
              </>
            ) : m.save_payment_method ? (
              <Chip>saves PayPal at the first checkout</Chip>
            ) : (
              <Chip>pays through PayPal checkout</Chip>
            )}
            <span className="muted num spent-line">{money(m.spent, m.currency)} spent</span>
          </div>
          <div className="label">{missions.length} mission{missions.length === 1 ? '' : 's'}</div>
          {mini.length === 0 ? (
            <div className="muted secondary">No missions under this mandate yet.</div>
          ) : (
            <div className="rows" role="list">
              {mini.map((ms) => (
                <div key={ms.id} className="row mini" role="listitem">
                  <span className="goal" title={ms.goal}><a href={`#/missions/${ms.id}`}>{ms.goal}</a></span>
                  <Pill status={ms.status} />
                  <span className="when num">{relativeTime(ms.updated_at)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </Card>
  )
}

export function StandingOrderButtons({ mission, onChanged }: { mission: Mission; onChanged: () => void }) {
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const act = async (what: 'run' | 'stop') => {
    setBusy(what)
    setErr('')
    try {
      if (what === 'run') {
        const run = await api.runNow(mission.id)
        navigate(`#/missions/${run.id}`)
      } else {
        await api.stopRepeat(mission.id)
      }
      onChanged()
    } catch (e: any) {
      setErr(e.message)
    } finally {
      setBusy('')
    }
  }
  return (
    <span className="actions" style={{ gap: 6 }}>
      <button className="btn ghost sm" aria-label={`Run now: ${mission.goal}`} onClick={() => act('run')} disabled={busy !== ''}>{busy === 'run' ? 'Starting…' : 'Run now'}</button>
      <button className="btn ghost sm" aria-label={`Stop: ${mission.goal}`} onClick={() => act('stop')} disabled={busy !== ''}>{busy === 'stop' ? 'Stopping…' : 'Stop'}</button>
      {err && <span className="alert bad" role="alert" style={{ padding: '2px 8px', margin: 0 }}>{err}</span>}
    </span>
  )
}

function ForgetButton({ mandate: m, onChanged }: { mandate: Mandate; onChanged: () => void }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const forget = async () => {
    if (!window.confirm('Forget the PayPal account saved on this mandate? Future carts go through PayPal checkout again.')) return
    setBusy(true)
    setErr('')
    try {
      await api.forgetPayment(m.id)
      onChanged()
    } catch (e: any) {
      setErr(e.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <button type="button" className="link" onClick={forget} disabled={busy} title="Revoke: the agent loses the ability to pay without you">
        {busy ? 'Forgetting…' : 'forget saved PayPal'}
      </button>
      {err && <span className="alert bad" role="alert" style={{ padding: '2px 8px', margin: 0 }}>{err}</span>}
    </>
  )
}
