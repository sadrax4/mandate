import { useEffect, useState, type FormEvent } from 'react'
import { api, money, type Mandate, type Mission } from '../api'
import { BudgetBar, Card, Chip, Empty, Pill, navigate } from '../components/ui'

const DEMO_GOALS: { goal: string; note?: string }[] = [
  { goal: 'A mechanical keyboard with brown switches and a USB-C hub with HDMI, both under $120' },
  { goal: 'Noise cancelling headphones for open-office work' },
  { goal: 'A large felt desk mat under $40' },
  { goal: 'A gooseneck kettle and a coffee grinder for pour-over', note: 'outside a computers/office/audio mandate: watch the agent explain' },
]

export default function Dashboard() {
  const [mandates, setMandates] = useState<Mandate[]>([])
  const [missions, setMissions] = useState<Mission[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')

  const load = () =>
    Promise.all([api.mandates(), api.missions()])
      .then(([m, ms]) => {
        setMandates(m)
        setMissions(ms)
      })
      .catch((e) => setError(e.message))

  useEffect(() => {
    load()
    const t = setInterval(load, 4000)
    return () => clearInterval(t)
  }, [])

  const createDemo = async () => {
    setBusy('demo')
    try {
      await api.createMandate({
        name: 'Desk refresh',
        currency: 'USD',
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

      {mandates.length === 0 ? (
        <Empty>No mandates yet. Create one, or use the demo mandate to see the whole flow in a minute.</Empty>
      ) : (
        <div className="mandate-grid">
          {mandates.map((m) => (
            <MandateCard key={m.id} mandate={m} missions={missions.filter((x) => x.mandate_id === m.id)} onStarted={load} />
          ))}
        </div>
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

function MandateCard({ mandate: m, missions, onStarted }: { mandate: Mandate; missions: Mission[]; onStarted: () => void }) {
  const [goal, setGoal] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const start = async (e: FormEvent) => {
    e.preventDefault()
    if (!goal.trim()) return
    setBusy(true)
    setErr('')
    try {
      const ms = await api.createMission(m.id, goal.trim())
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
          <Chip tone="good">PayPal saved · one-click approvals{m.auto_approve_under > 0 && !m.require_approval ? `, auto-pay under ${money(m.auto_approve_under, m.currency)}` : ''}</Chip>
        ) : m.save_payment_method ? (
          <Chip>saves PayPal at the first checkout</Chip>
        ) : null}
      </div>
      <form className="goal-form" onSubmit={start}>
        <input value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="What should the agent buy? e.g. a 27-inch 4K USB-C monitor" />
        <button className="btn primary" disabled={busy || !goal.trim()}>{busy ? 'Starting…' : 'Start mission'}</button>
      </form>
      <div className="suggestions">
        {DEMO_GOALS.map((g) => (
          <button key={g.goal} type="button" className="link" title={g.note} onClick={() => setGoal(g.goal)}>
            {g.goal}
            {g.note && <span className="muted"> ({g.note})</span>}
          </button>
        ))}
      </div>
      {err && <div className="alert bad">{err}</div>}
    </Card>
  )
}
