import { useMemo } from 'react'
import { AgCharts } from 'ag-charts-react'
import type { AgCartesianChartOptions } from 'ag-charts-community'
import { money, type Negotiation } from '../api'
import { Chip } from './ui'
import { usePrefersDark } from './Grid'

/** The outcome chip shown next to the negotiation heading. */
export function negotiationChip(n: Negotiation, currency: string, running: boolean) {
  const saved = n.agreed_price ? n.list_price - n.agreed_price : 0
  if (n.outcome === 'agreed') return <Chip tone="good">deal at {money(n.agreed_price ?? 0, currency)} · {Math.round((100 * saved) / n.list_price)}% below asking</Chip>
  if (n.outcome) return <Chip>no deal</Chip>
  return <Chip>{running ? 'negotiating…' : 'open'}</Chip>
}

/** Transcript and price chart of a buyer agent negotiating with a seller agent (no card of its own). */
export function NegotiationBlock({ negotiation: n, currency, running }: { negotiation: Negotiation; currency: string; running: boolean }) {
  const isDark = usePrefersDark()
  const rounds = n.rounds ?? []
  const rows = useMemo(() => {
    const out: { round: string; buyer?: number; seller?: number }[] = []
    let cur: { round: string; buyer?: number; seller?: number } | null = null
    for (const r of rounds) {
      if (!cur || cur.round !== String(r.round)) {
        cur = { round: String(r.round) }
        out.push(cur)
      }
      if (r.price && (r.move === 'offer' || r.move === 'counter' || r.move === 'accept')) {
        if (r.party === 'buyer') cur.buyer = r.price
        else cur.seller = r.price
      }
    }
    return out
  }, [rounds])
  const chart = useMemo<AgCartesianChartOptions>(
    () => ({
      theme: isDark ? 'ag-default-dark' : 'ag-default',
      background: { fill: 'transparent' },
      padding: { top: 8, right: 12, bottom: 4, left: 8 },
      height: 220,
      data: rows,
      series: [
        { type: 'line', xKey: 'round', yKey: 'seller', yName: 'Seller asks', stroke: '#2563eb', marker: { fill: '#2563eb', stroke: '#2563eb' }, connectMissingData: true },
        { type: 'line', xKey: 'round', yKey: 'buyer', yName: 'Buyer offers', stroke: '#0d9488', marker: { fill: '#0d9488', stroke: '#0d9488' }, connectMissingData: true },
      ],
      axes: {
        x: { type: 'category', title: { text: 'round' } },
        y: { type: 'number', label: { formatter: (p: any) => money(p.value, currency) }, crossLines: [{ type: 'line', value: n.limit, stroke: '#d97706', lineDash: [6, 4], label: { text: `mandate limit ${money(n.limit, currency)}`, position: 'top', color: '#d97706' } }] },
      },
      legend: { position: 'bottom' },
    }),
    [rows, isDark, currency, n.limit],
  )
  return (
    <div className="negotiation">
      <p className="muted secondary" style={{ margin: '0 0 6px' }}>
        Buyer agent limit {money(n.limit, currency)}, never revealed · seller asked {money(n.list_price, currency)} · neither agent can pay; the agreed price becomes a proposal for you.
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
      {rows.length > 1 && <div className="chart" style={{ height: 220 }}><AgCharts options={chart} /></div>}
    </div>
  )
}
