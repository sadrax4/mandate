#!/usr/bin/env node
// Calls one tool on PayPal's hosted MCP server directly (no Claude, no Anthropic MCP connector) and prints
// PayPal's own answer. Use it to tell "PayPal's MCP is down" from "Mandate's assistant is wrong".
//
//   set -a; . /opt/mandate/.env; set +a          # your PayPal sandbox keys stay on your machine
//   node scripts/mcpcheck.cjs                    # lists the tools
//   node scripts/mcpcheck.cjs get_order 2UC584867E9881619
//   node scripts/mcpcheck.cjs list_transactions  '{"start_date":"2026-10-25T00:00:00Z","end_date":"2026-11-02T00:00:00Z"}'
//
// A second argument that is not JSON is used as the value of the tool's first required parameter.
// Needs Node 18+. MCP_URL overrides the endpoint (default https://mcp.sandbox.paypal.com/sse).
const id = process.env.PAYPAL_CLIENT_ID, secret = process.env.PAYPAL_CLIENT_SECRET
const base = process.env.PAYPAL_BASE || 'https://api-m.sandbox.paypal.com'
const sseURL = process.env.MCP_URL || 'https://mcp.sandbox.paypal.com/sse'
const [tool, arg] = process.argv.slice(2)
if (!id || !secret) { console.error('PAYPAL_CLIENT_ID / PAYPAL_CLIENT_SECRET are not set (source your .env first)'); process.exit(2) }

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
async function main() {
  const tr = await fetch(base + '/v1/oauth2/token', { method: 'POST', headers: { Authorization: 'Basic ' + Buffer.from(id + ':' + secret).toString('base64'), 'Content-Type': 'application/x-www-form-urlencoded' }, body: 'grant_type=client_credentials' })
  if (!tr.ok) throw new Error('oauth token: HTTP ' + tr.status)
  const token = (await tr.json()).access_token
  const auth = { Authorization: 'Bearer ' + token }

  const sse = await fetch(sseURL, { headers: { ...auth, Accept: 'text/event-stream' } })
  if (!sse.ok) throw new Error('sse connect: HTTP ' + sse.status + ' ' + (await sse.text()).slice(0, 200))
  const reader = sse.body.getReader(), dec = new TextDecoder()
  let buf = '', endpoint = null
  const waiting = new Map()
  const feed = (chunk) => {
    buf += chunk
    let i
    while ((i = buf.search(/\r?\n\r?\n/)) >= 0) {
      const block = buf.slice(0, i); buf = buf.slice(i).replace(/^\r?\n\r?\n/, '')
      let ev = '', data = []
      for (const line of block.split(/\r?\n/)) { if (line.startsWith('event:')) ev = line.slice(6).trim(); else if (line.startsWith('data:')) data.push(line.slice(5).trim()) }
      const d = data.join('\n')
      if (ev === 'endpoint') endpoint = new URL(d, sseURL).toString()
      else { try { const m = JSON.parse(d); if (m.id != null && waiting.has(m.id)) { waiting.get(m.id)(m); waiting.delete(m.id) } } catch {} }
    }
  }
  ;(async () => { for (;;) { const { value, done } = await reader.read(); if (done) break; feed(dec.decode(value, { stream: true })) } })().catch(() => {})
  for (let n = 0; !endpoint && n < 100; n++) await sleep(100)
  if (!endpoint) throw new Error('sse: no endpoint event')

  let next = 1
  const rpc = async (method, params, notify) => {
    const msg = { jsonrpc: '2.0', method, params }
    if (!notify) msg.id = next++
    const p = notify ? null : new Promise((res, rej) => { waiting.set(msg.id, res); setTimeout(() => rej(new Error('timeout waiting for ' + method)), 60000) })
    const r = await fetch(endpoint, { method: 'POST', headers: { ...auth, 'Content-Type': 'application/json' }, body: JSON.stringify(msg) })
    if (r.status >= 300) throw new Error(method + ': HTTP ' + r.status + ' ' + (await r.text()).slice(0, 300))
    const body = await r.text()
    if (!notify && body.startsWith('{')) { try { const m = JSON.parse(body); if (m.id === msg.id) { waiting.delete(msg.id); return m } } catch {} }
    return p
  }
  await rpc('initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'mandate-mcpcheck', version: '1.0' } })
  await rpc('notifications/initialized', {}, true)
  const listed = (await rpc('tools/list', {})).result?.tools ?? []
  if (!tool) { for (const t of listed) console.log(t.name); console.log(`\n${listed.length} tools`); process.exit(0) }
  const def = listed.find((t) => t.name === tool)
  if (!def) throw new Error('no tool named ' + tool)
  let args = {}
  if (arg) { try { args = JSON.parse(arg) } catch { const key = def.inputSchema?.required?.[0] ?? Object.keys(def.inputSchema?.properties ?? {})[0]; args = { [key]: arg } } }
  console.log('tool', tool, 'schema', JSON.stringify(def.inputSchema?.properties ? Object.keys(def.inputSchema.properties) : []), 'args', JSON.stringify(args))
  const res = await rpc('tools/call', { name: tool, arguments: args })
  console.log(JSON.stringify(res, null, 2).slice(0, 4000))
  process.exit(0)
}
main().catch((e) => { console.error('FAILED:', e.message); process.exit(1) })
