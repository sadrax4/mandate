import { useMemo } from 'react'
import { AgCharts } from 'ag-charts-react'
import type { AgCartesianChartOptions } from 'ag-charts-community'
import { money, type Negotiation } from '../api'
import { Card, Chip } from './ui'
import { usePrefersDark } from './Grid'

/** Transcript and price chart of a buyer agent negotiating with a seller agent. */
export function NegotiationCard({ negotiation: n, currency, running }: { negotiation: Negotiation; currency: string; running: boolean }) {
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
  const saved = n.agreed_price ? n.list_price - n.agreed_price : 0
  return (
    <Card
      title="Agent-to-agent negotiation"
      className="mt-0"
      action={
        n.outcome === 'agreed' ? (
          <Chip tone="good">deal at {money(n.agreed_price ?? 0, currency)} · {Math.round((100 * saved) / n.list_price)}% below asking</Chip>
        ) : n.outcome ? (
          <Chip>no deal</Chip>
        ) : (
          <Chip>{running ? 'negotiating…' : 'open'}</Chip>
        )
      }
    >
      <p className="muted" style={{ marginTop: 0 }}>
        The buyer agent works for you under the mandate (hard limit {money(n.limit, currency)}, never revealed); the seller agent asked {money(n.list_price, currency)}. Neither agent can pay: the agreed price becomes a proposal for you to approve.
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
    </Card>
  )
}
