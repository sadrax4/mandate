import { useEffect, useState, type FormEvent } from 'react'
import { useConfig } from '../config'
import { api } from '../api'
import { Card, navigate } from '../components/ui'

export default function MandateForm() {
  const config = useConfig()
  const [categories, setCategories] = useState<string[]>([])
  const [name, setName] = useState('')
  const [budget, setBudget] = useState('250')
  const [maxItem, setMaxItem] = useState('120')
  const [autoUnder, setAutoUnder] = useState('0')
  const [minRating, setMinRating] = useState('4')
  const [allowed, setAllowed] = useState<string[]>([])
  const [blocked, setBlocked] = useState('')
  const [requireApproval, setRequireApproval] = useState(false)
  const [savePayment, setSavePayment] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api.categories().then(setCategories).catch(() => {})
  }, [])

  const toggle = (c: string) => setAllowed((a) => (a.includes(c) ? a.filter((x) => x !== c) : [...a, c]))

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      await api.createMandate({
        name,
        currency: config?.currency || 'USD',
        total_budget: Number(budget),
        max_per_item: Number(maxItem) || 0,
        auto_approve_under: Number(autoUnder) || 0,
        min_rating: Number(minRating) || 0,
        allowed_categories: allowed,
        blocked_merchants: blocked.split(',').map((s) => s.trim()).filter(Boolean),
        require_approval: requireApproval,
        save_payment_method: savePayment,
      })
      navigate('#/')
    } catch (e: any) {
      setErr(e.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="page narrow">
      <div className="page-head">
        <div>
          <h1>New mandate</h1>
          <p className="lede">Everything the agent proposes is checked against these rules before a human sees it, and again at approval time. In the words of PayPal&apos;s Agent Payments Protocol this is an <b>intent mandate</b>: the conditions under which the agent may act when you are not present. Every cart you approve by hand is a <b>cart mandate</b>.</p>
        </div>
      </div>
      <form onSubmit={submit}>
        <Card title="Authority">
          <label className="field">
            <span>Name</span>
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Home office refresh" required />
          </label>
          <div className="field-row">
            <label className="field">
              <span>Total budget ({config?.currency || 'USD'})</span>
              <input type="number" min="1" step="0.01" value={budget} onChange={(e) => setBudget(e.target.value)} required />
            </label>
            <label className="field">
              <span>Max per item (0 = no cap)</span>
              <input type="number" min="0" step="0.01" value={maxItem} onChange={(e) => setMaxItem(e.target.value)} />
            </label>
          </div>
          <div className="field-row">
            <label className="field">
              <span>Auto-approve carts under (0 = always ask me)</span>
              <input type="number" min="0" step="0.01" value={autoUnder} onChange={(e) => setAutoUnder(e.target.value)} />
            </label>
            <label className="field">
              <span>Minimum rating (0 = ignore)</span>
              <input type="number" min="0" max="5" step="0.1" value={minRating} onChange={(e) => setMinRating(e.target.value)} />
            </label>
          </div>
          <label className="check">
            <input type="checkbox" checked={requireApproval} onChange={(e) => setRequireApproval(e.target.checked)} />
            <span>Always require my approval, even under the auto-approve threshold</span>
          </label>
          <label className="check">
            <input type="checkbox" checked={savePayment} onChange={(e) => setSavePayment(e.target.checked)} />
            <span>
              Save my PayPal account at the first checkout (PayPal vault). After that, carts I approve are paid with one click and carts under the
              auto-approve threshold are paid with no click at all, always inside this mandate.
            </span>
          </label>
        </Card>
        <Card title="Rules" className="mt">
          <div className="field">
            <span>Allowed categories (none selected = any)</span>
            <div className="checks">
              {categories.map((c) => (
                <label key={c} className={`check-chip ${allowed.includes(c) ? 'on' : ''}`}>
                  <input type="checkbox" checked={allowed.includes(c)} onChange={() => toggle(c)} />
                  {c}
                </label>
              ))}
            </div>
          </div>
          <label className="field">
            <span>Blocked merchants (comma separated)</span>
            <input value={blocked} onChange={(e) => setBlocked(e.target.value)} placeholder="e.g. BudgetBytes, ShadyShop" />
          </label>
        </Card>
        {err && <div className="alert bad mt">{err}</div>}
        <div className="actions mt">
          <a className="btn ghost" href="#/">Cancel</a>
          <button className="btn primary" disabled={busy || !name.trim()}>{busy ? 'Saving…' : 'Create mandate'}</button>
        </div>
      </form>
    </div>
  )
}
