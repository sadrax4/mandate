import { useMemo } from 'react'
import { AgCharts } from 'ag-charts-react'
import type { AgCartesianChartOptions } from 'ag-charts-community'
import { money, type Negotiation } from '../api'
import { Chip } from './ui'
import { chartTheme, useChartTokens } from './Grid'

/** The outcome chip shown next to the negotiation heading; `short` leaves out the price when the heading already shows it. */
export function negotiationChip(n: Negotiation, currency: string, running: boolean, short = false) {
  const saved = n.agreed_price ? n.list_price - n.agreed_price : 0
  const pct = `${Math.round((100 * saved) / n.list_price)}% below asking`
  if (n.outcome === 'agreed') return <Chip tone="good">{short ? `negotiated · ${pct}` : `deal at ${money(n.agreed_price ?? 0, currency)} · ${pct}`}</Chip>
  if (n.outcome) return <Chip>no deal</Chip>
  return <Chip>{running ? 'negotiating…' : 'open'}</Chip>
}

/** A round step that keeps a chart to about four labelled values. */
function niceStep(span: number) {
  const raw = Math.max(span / 4, 1)
  const pow = 10 ** Math.floor(Math.log10(raw))
  const f = raw / pow
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * pow
}

/** Transcript and price chart of a buyer agent negotiating with a seller agent (no card of its own). */
export function NegotiationBlock({ negotiation: n, currency, running }: { negotiation: Negotiation; currency: string; running: boolean }) {
  const t = useChartTokens()
  const rounds = n.rounds ?? []
  const rows = useMemo(() => {
    const out: { round: string; buyer?: number; seller?: number }[] = []
    let cur: { round: string; buyer?: number; seller?: number } | null = null
    for (const r of rounds) {
      const key = `R${r.round + 1}`
      if (!cur || cur.round !== key) {
        cur = { round: key }
        out.push(cur)
      }
      if (r.price && (r.move === 'offer' || r.move === 'counter' || r.move === 'accept')) {
        if (r.party === 'buyer') cur.buyer = r.price
        else cur.seller = r.price
      }
    }
    return out
  }, [rounds])
  const chart = useMemo<AgCartesianChartOptions>(() => {
    const prices = rows.flatMap((r) => [r.buyer, r.seller]).filter((v): v is number => typeof v === 'number')
    const lo = Math.min(n.limit, ...prices)
    const hi = Math.max(n.list_price, ...prices)
    const step = niceStep(hi - lo)
    const yMin = Math.floor(lo / step) * step - step / 2
    // The limit's label goes on the side of the line with more room before the next grid line.
    const frac = ((((n.limit - yMin) % step) + step) % step) / step
    const labelPos = frac < 0.5 ? 'top-left' : 'bottom-left'
    return {
      theme: chartTheme(t),
      background: { fill: 'transparent' },
      padding: { top: 8, right: 12, bottom: 0, left: 4 },
      height: 180,
      data: rows,
      series: [
        { type: 'line', xKey: 'round', yKey: 'seller', yName: 'Seller asks', stroke: t.live, marker: { fill: t.live, stroke: t.live }, connectMissingData: true },
        { type: 'line', xKey: 'round', yKey: 'buyer', yName: 'Buyer offers', stroke: t.accent, marker: { fill: t.accent, stroke: t.accent }, connectMissingData: true },
      ],
      axes: {
        x: { type: 'category' },
        y: {
          type: 'number',
          min: yMin,
          max: Math.ceil(hi / step) * step,
          interval: { step },
          label: { formatter: (p: any) => money(p.value, currency) },
          crossLines: [{ type: 'line', value: n.limit, stroke: t.warn, lineDash: [6, 4], label: { text: `limit ${money(n.limit, currency)}`, position: labelPos, fontSize: 11, color: t.warn } }],
        },
      },
      legend: { position: 'bottom', item: { marker: { size: 8 } } },
    }
  }, [rows, t, currency, n.limit, n.list_price])
  return (
    <div className="negotiation">
      <p className="muted secondary" style={{ margin: '0 0 6px' }}>
        Your agent's limit, {money(n.limit, currency)}, is never revealed. The seller asked {money(n.list_price, currency)}. Neither agent can pay: the agreed price becomes a proposal for you.
      </p>
      <div className="transcript">
        {rounds.map((r, i) => (
          <div key={i} className={`bubble ${r.party}`}>
            <div className="who">
              {r.party === 'buyer' ? 'Buyer agent' : n.seller}
              {r.price ? <b> · {r.move === 'accept' ? 'accepts' : r.move === 'offer' ? 'offers' : r.move === 'counter' ? 'asks' : r.move} {money(r.price, currency)}</b> : <b> · {r.move.replace('_', ' ')}</b>}
            </div>
            <div>{r.message}</div>
          </div>
        ))}
        {running && <div className="muted">…</div>}
      </div>
      {rows.length > 1 && (
        <div className="chart" style={{ height: 180 }} role="img" aria-label={`Price per round: seller from ${money(n.list_price, currency)}${n.agreed_price ? `, agreed at ${money(n.agreed_price, currency)}` : ''}, your limit ${money(n.limit, currency)}`}>
          <AgCharts options={chart} />
        </div>
      )}
    </div>
  )
}
