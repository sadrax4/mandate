import { useEffect, useState, type FormEvent } from 'react'
import { api, money, type Mandate, type Mission } from '../api'
import { useConfig } from '../config'
import { BudgetBar, Card, Chip, Empty, Pill, navigate } from '../components/ui'

const DEMO_GOALS: { goal: string; note?: string; kind?: string }[] = [
  { goal: 'A mechanical keyboard with brown switches and a USB-C hub with HDMI, both under $120' },
  { goal: 'Noise cancelling headphones for open-office work' },
  { goal: 'A large felt desk mat under $40' },
  { goal: 'A gooseneck kettle and a coffee grinder for pour-over', note: 'outside a computers/office/audio mandate: watch the agent explain' },
  { goal: 'A two-hour product photo shoot for 20 items', kind: 'negotiate', note: 'two agents negotiate the price, you approve, PayPal settles' },
]

export default function Dashboard() {
  const [mandates, setMandates] = useState<Mandate[]>([])
  const [missions, setMissions] = useState<Mission[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const config = useConfig()
  const [showAll, setShowAll] = useState(false)
  const [guideHidden, setGuideHidden] = useState(() => {
    try {
      return localStorage.getItem('mandate.guide.hidden') === '1'
    } catch {
      return false
    }
  })
  const hideGuide = () => {
    setGuideHidden(true)
    try {
      localStorage.setItem('mandate.guide.hidden', '1')
    } catch {
      /* private mode */
    }
  }

  const load = () =>
    Promise.all([api.mandates(), api.missions()])
      .then(([m, ms]) => {
        setMandates(m)
        setMissions(ms)
        setError('')
      })
      .catch((e) => setError(e.message))

  useEffect(() => {
    load()
    const t = setInterval(load, 4000)
    return () => clearInterval(t)
  }, [])
  const VISIBLE = 6
  const sorted = [...mandates].sort((a, b) => (a.created_at < b.created_at ? 1 : -1))
  const visible = showAll ? sorted : sorted.slice(0, VISIBLE)

  const createDemo = async () => {
    setBusy('demo')
    try {
      await api.createMandate({
        name: 'Desk refresh',
        currency: config?.currency || 'USD',
        total_budget: 300,
        max_per_item: 150,
        auto_approve_under: 40,
        allowed_categories: ['computers', 'office', 'audio'],
        blocked_merchants: ['temu.com'],
        min_rating: 4,
        save_payment_method: true,
      })
      await load()
    } catch (e: any) {
      setError(e.message)
    } finally {
      setBusy('')
    }
  }

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1>Mandates</h1>
          <p className="lede">A mandate is the authority you give the agent: a budget, caps and rules. Start a mission under a mandate and the agent researches, compares and proposes. You approve. PayPal pays.</p>
        </div>
        <div className="actions">
          <button className="btn ghost" onClick={createDemo} disabled={busy === 'demo'}>
            {busy === 'demo' ? 'Creating…' : 'Create demo mandate'}
          </button>
          <a className="btn primary" href="#/mandates/new">New mandate</a>
        </div>
      </div>
      {error && <div className="alert bad">{error}</div>}

      {!guideHidden && (
        <div className="guide">
          <div>
            <b>Start here (two minutes).</b>
            <ol>
              <li><b>Create demo mandate</b>, or set your own limits with <b>New mandate</b>.</li>
              <li>Pick a suggested goal and <b>Start mission</b>. Watch the agent search{config?.catalog === 'channel3' ? ' real products through Channel3' : ' the demo catalog'}, check the mandate and propose.</li>
              <li><b>Approve & pay with PayPal</b>{config?.paypal_mode === 'mock' ? ' (a built-in simulator stands in for PayPal here)' : config?.demo_buyer_hint ? <> and log in with the sandbox buyer <code>{config.demo_buyer_hint}</code></> : ''}. Back here you get the capture ID, the Ledger row and the Insights charts.</li>
              <li>Then try: approve a second cart with one click (the mandate saved your PayPal account), reject a proposal with feedback, start a goal the mandate cannot fit and accept the agent's suggested fix, or make a mission a monthly standing order.</li>
            </ol>
            <span className="muted">Nothing here moves real money: PayPal runs in {config?.paypal_mode === 'mock' ? 'simulated' : 'sandbox'} mode.</span>
          </div>
          <button className="btn ghost sm" onClick={hideGuide} aria-label="Hide this guide">Hide</button>
        </div>
      )}

      {missions.some((ms) => ms.status === 'awaiting_approval') && (
        <Card title="Needs your approval" className="mt-0" action={<span className="muted">the agent proposed; nothing moves until you decide</span>}>
          <table className="table">
            <thead>
              <tr>
                <th>Goal</th>
                <th>Mandate</th>
                <th>Proposed</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {missions.filter((ms) => ms.status === 'awaiting_approval').map((ms) => (
                <tr key={ms.id}>
                  <td>{ms.goal}</td>
                  <td>{mandates.find((m) => m.id === ms.mandate_id)?.name ?? '—'}</td>
                  <td className="muted">{new Date(ms.updated_at).toLocaleString()}</td>
                  <td><a className="btn primary sm" href={`#/missions/${ms.id}`}>Review &amp; approve</a></td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}

      {mandates.length === 0 ? (
        <Empty>No mandates yet. Create one, or use the demo mandate to see the whole flow in a minute.</Empty>
      ) : (
        <>
          <div className="mandate-grid">
            {visible.map((m) => (
              <MandateCard key={m.id} mandate={m} missions={missions.filter((x) => x.mandate_id === m.id)} onStarted={load} />
            ))}
          </div>
          {sorted.length > VISIBLE && (
            <div className="actions" style={{ justifyContent: 'center', marginTop: 10 }}>
              <button className="btn ghost" onClick={() => setShowAll((v) => !v)}>
                {showAll ? `Show the ${VISIBLE} newest mandates` : `Show all ${sorted.length} mandates`}
              </button>
            </div>
          )}
        </>
      )}

      {missions.some((ms) => (ms.repeat_days ?? 0) > 0) && (
        <Card title="Standing orders" className="mt" action={<span className="muted">goals the agent re-runs on a schedule, always inside the mandate</span>}>
          <table className="table">
            <thead>
              <tr>
                <th>Goal</th>
                <th>Mandate</th>
                <th>Every</th>
                <th>Next run</th>
                <th>Last run</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {missions.filter((ms) => (ms.repeat_days ?? 0) > 0).map((ms) => {
                const last = ms.last_run_id ? missions.find((x) => x.id === ms.last_run_id) : ms
                return (
                  <tr key={ms.id}>
                    <td><a href={`#/missions/${ms.id}`}>{ms.goal}</a></td>
                    <td>{mandates.find((m) => m.id === ms.mandate_id)?.name ?? '—'}</td>
                    <td>{ms.repeat_days} days</td>
                    <td className="muted">{ms.next_run_at ? new Date(ms.next_run_at).toLocaleDateString() : '—'}</td>
                    <td>{last ? <a href={`#/missions/${last.id}`}><Pill status={last.status} /></a> : '—'}</td>
                    <td><StandingOrderButtons mission={ms} onChanged={load} /></td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </Card>
      )}

      <Card title="Recent missions" className="mt">
        {missions.length === 0 ? (
          <Empty>No missions yet.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Goal</th>
                <th>Mandate</th>
                <th>Status</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {missions.slice(0, 20).map((ms) => (
                <tr key={ms.id} className="row-link" onClick={() => navigate(`#/missions/${ms.id}`)}>
                  <td>{ms.goal}</td>
                  <td>{mandates.find((m) => m.id === ms.mandate_id)?.name ?? '—'}</td>
                  <td><Pill status={ms.status} /></td>
                  <td className="muted">{new Date(ms.updated_at).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  )
}

const REPEAT_OPTIONS = [
  { days: 0, label: 'Run once' },
  { days: 7, label: 'Repeat weekly' },
  { days: 30, label: 'Repeat monthly' },
  { days: 90, label: 'Repeat quarterly' },
]

function MandateCard({ mandate: m, missions, onStarted }: { mandate: Mandate; missions: Mission[]; onStarted: () => void }) {
  const [goal, setGoal] = useState('')
  const [repeat, setRepeat] = useState(0)
  const [kind, setKind] = useState('buy')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const start = async (e: FormEvent) => {
    e.preventDefault()
    if (!goal.trim()) return
    setBusy(true)
    setErr('')
    try {
      const ms = await api.createMission(m.id, goal.trim(), repeat, kind)
      onStarted()
      navigate(`#/missions/${ms.id}`)
    } catch (e: any) {
      setErr(e.message)
    } finally {
      setBusy(false)
    }
  }
  const active = missions.filter((x) => x.status === 'running' || x.status === 'awaiting_approval' || x.status === 'awaiting_payment').length
  return (
    <Card title={m.name} action={<span className="muted">{missions.length} mission{missions.length === 1 ? '' : 's'}{active ? ` · ${active} active` : ''}</span>}>
      <BudgetBar total={m.total_budget} spent={m.spent} reserved={m.reserved} currency={m.currency} />
      <div className="chips">
        {m.max_per_item > 0 && <Chip>≤ {money(m.max_per_item, m.currency)} per item</Chip>}
        {m.auto_approve_under > 0 ? <Chip>auto-approve under {money(m.auto_approve_under, m.currency)}</Chip> : <Chip>every cart needs approval</Chip>}
        {m.allowed_categories && m.allowed_categories.length > 0 && <Chip>only {m.allowed_categories.join(', ')}</Chip>}
        {m.blocked_merchants && m.blocked_merchants.length > 0 && <Chip>never {m.blocked_merchants.join(', ')}</Chip>}
        {m.min_rating > 0 && <Chip>rating ≥ {m.min_rating}</Chip>}
        {m.vault_id ? (
          <>
            <Chip tone="good">PayPal saved · one-click approvals{m.auto_approve_under > 0 && !m.require_approval ? `, auto-pay under ${money(m.auto_approve_under, m.currency)}` : ''}</Chip>
            <ForgetButton mandate={m} onChanged={onStarted} />
          </>
        ) : m.save_payment_method ? (
          <Chip>saves PayPal at the first checkout</Chip>
        ) : null}
      </div>
      <form className="goal-form" onSubmit={start}>
        <input value={goal} onChange={(e) => setGoal(e.target.value)} placeholder={kind === 'negotiate' ? 'What service should the agents negotiate? e.g. logo design, three concepts' : 'What should the agent buy? e.g. a 27-inch 4K USB-C monitor'} aria-label={`Goal for the agent under ${m.name}`} />
        <select value={kind} onChange={(e) => setKind(e.target.value)} aria-label="Mission kind" title="Buy: research a catalog and propose a cart. Negotiate: a buyer agent haggles with a seller agent for a service; the agreed price becomes the proposal.">
          <option value="buy">Buy a product</option>
          <option value="negotiate">Hire a service (agents negotiate)</option>
        </select>
        <select value={repeat} onChange={(e) => setRepeat(Number(e.target.value))} aria-label="Repeat schedule" title="Standing order: run this goal again on a schedule, inside the same mandate">
          {REPEAT_OPTIONS.map((o) => <option key={o.days} value={o.days}>{o.label}</option>)}
        </select>
        <button className="btn primary" disabled={busy || !goal.trim()}>{busy ? 'Starting…' : 'Start mission'}</button>
      </form>
      <div className="suggestions">
        {DEMO_GOALS.map((g) => (
          <button key={g.goal} type="button" className="link" title={g.note} onClick={() => { setGoal(g.goal); setKind(g.kind ?? 'buy') }}>
            {g.goal}
            {g.note && <span className="muted"> ({g.note})</span>}
          </button>
        ))}
      </div>
      {err && <div className="alert bad">{err}</div>}
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
      <button className="btn ghost sm" onClick={() => act('run')} disabled={busy !== ''}>{busy === 'run' ? 'Starting…' : 'Run now'}</button>
      <button className="btn ghost sm" onClick={() => act('stop')} disabled={busy !== ''}>{busy === 'stop' ? 'Stopping…' : 'Stop'}</button>
      {err && <span className="alert bad" style={{ padding: '2px 8px' }}>{err}</span>}
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
      {err && <span className="alert bad" style={{ padding: '2px 8px' }}>{err}</span>}
    </>
  )
}
