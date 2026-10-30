// End-to-end browser smoke test. Run the server in offline demo mode first:
//   PAYPAL_MODE=mock LLM_PROVIDER=fake DATA_FILE=/tmp/mandate-smoke.json go run ./cmd/mandate
// then: BASE_URL=http://localhost:8080 SHOTS=/tmp/shots node scripts/smoke.cjs
// Requires: npm i -g playwright && npx playwright install chromium
const { chromium } = require('playwright'); (async () => {
const base = process.env.BASE_URL || 'http://localhost:8080'
const S = process.env.SHOTS || 'docs/screenshots'
const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1360, height: 900 } })
let pageErrors = 0
page.on('pageerror', (e) => { pageErrors++; console.log('PAGEERROR', e.message) })
page.on('console', (m) => { if (m.type() === 'error') console.log('CONSOLE', m.text()) })

// every page must render on a fresh install (empty lists, no data)
for (const h of ['#/insights', '#/ledger', '#/mandates/new']) {
  await page.goto(base + '/' + h)
  await page.waitForTimeout(500)
}
if (pageErrors) throw new Error('page errors on a fresh install')

await page.goto(base + '/#/')
await page.waitForSelector('text=Mandates')
await page.click('text=Create demo mandate')
await page.waitForSelector('text=Desk refresh')
await page.screenshot({ path: `${S}/01-dashboard.png`, fullPage: true })

await page.click('text=A mechanical keyboard with brown switches')
await page.click('button:has-text("Start mission")')
await page.waitForURL(/#\/missions\//)
await page.waitForSelector('text=Approve & pay with PayPal', { timeout: 20000 })
await page.waitForTimeout(800)
await page.screenshot({ path: `${S}/02-mission-proposal.png`, fullPage: true })

await page.click('button:has-text("Approve & pay with PayPal")')
await page.waitForSelector('text=Pay with PayPal (simulated)', { timeout: 10000 })
await page.screenshot({ path: `${S}/03-mock-paypal.png`, fullPage: true })
await page.click('button:has-text("Approve and pay")')
await page.waitForSelector('text=Payment captured', { timeout: 10000 })
await page.waitForTimeout(800)
await page.screenshot({ path: `${S}/04-mission-paid.png`, fullPage: true })

await page.goto(base + '/#/ledger')
await page.waitForSelector('.ag-row', { timeout: 10000 })
await page.waitForTimeout(500)
await page.screenshot({ path: `${S}/05-ledger.png`, fullPage: true })

// rejection flow on a fresh mandate with enough budget
const res = await page.request.post(base + '/api/mandates', { data: { name: 'Audio upgrade', currency: 'USD', total_budget: 400, max_per_item: 400, allowed_categories: ['audio'], min_rating: 4 } })
const audio = await res.json()
const ms = await (await page.request.post(base + '/api/missions', { data: { mandate_id: audio.id, goal: 'noise cancelling headphones' } })).json()
await page.goto(base + '/#/missions/' + ms.id)
await page.waitForSelector('text=Reject with feedback', { timeout: 20000 })
await page.click('text=Reject with feedback')
await page.fill('textarea', 'too expensive, find something cheaper')
await page.click('text=Send back to the agent')
await page.waitForSelector('text=Earlier proposals', { timeout: 20000 })
await page.waitForTimeout(800)
await page.screenshot({ path: `${S}/06-mission-after-reject.png`, fullPage: true })

// a goal outside the mandate: the agent explains, suggests the smallest change, the human amends and retries
const tight = await (await page.request.post(base + '/api/mandates', { data: { name: 'Office only', currency: 'USD', total_budget: 300, max_per_item: 300, allowed_categories: ['computers', 'office'] } })).json()
const ms2 = await (await page.request.post(base + '/api/missions', { data: { mandate_id: tight.id, goal: 'a gooseneck kettle for pour-over' } })).json()
await page.goto(base + '/#/missions/' + ms2.id)
await page.waitForSelector('text=The agent suggests:', { timeout: 20000 })
await page.waitForTimeout(600)
await page.screenshot({ path: `${S}/10-suggestion.png`, fullPage: true })
await page.click('button:has-text("Amend mandate & retry")')
await page.waitForURL((u) => u.hash.startsWith('#/missions/') && !u.hash.includes(ms2.id), { timeout: 10000 })
await page.waitForSelector('text=Retry of', { timeout: 10000 })
await page.waitForSelector('text=Approve & pay with PayPal', { timeout: 20000 })
const amended = await (await page.request.get(base + '/api/mandates/' + tight.id)).json()
if (!amended.mandate.allowed_categories.includes('kitchen')) throw new Error('mandate was not amended: ' + JSON.stringify(amended.mandate.allowed_categories))

// standing order: the goal re-runs on a schedule; "Run now" starts a linked run
const so = await (await page.request.post(base + '/api/missions', { data: { mandate_id: audio.id, goal: 'noise cancelling headphones', repeat_days: 30 } })).json()
await page.goto(base + '/#/missions/' + so.id)
await page.waitForSelector('text=Standing order:', { timeout: 10000 })
await page.click('button:has-text("Run now")')
await page.waitForURL((u) => u.hash.startsWith('#/missions/') && !u.hash.includes(so.id), { timeout: 10000 })
await page.waitForSelector('text=Run of', { timeout: 10000 })
await page.goto(base + '/#/')
await page.waitForSelector('text=Standing orders', { timeout: 10000 })
await page.waitForTimeout(800)
await page.screenshot({ path: `${S}/11-standing-orders.png`, fullPage: true })

// agent-to-agent: a buyer agent negotiates a service with a seller agent, inside the mandate
const svc = await (await page.request.post(base + '/api/mandates', { data: { name: 'Freelance', currency: 'USD', total_budget: 600, max_per_item: 200 } })).json()
const nm = await (await page.request.post(base + '/api/missions', { data: { mandate_id: svc.id, goal: 'a two-hour product photo shoot for 20 items', kind: 'negotiate' } })).json()
await page.goto(base + '/#/missions/' + nm.id)
await page.waitForSelector('text=Agent-to-agent negotiation', { timeout: 20000 })
await page.waitForSelector('text=deal at', { timeout: 20000 })
await page.waitForSelector('text=Approve & pay with PayPal', { timeout: 20000 })
await page.waitForTimeout(1200)
await page.screenshot({ path: `${S}/13-negotiation.png`, fullPage: true })
const nd = await (await page.request.get(base + '/api/missions/' + nm.id)).json()
if (nd.mission.negotiation.agreed_price > 200) throw new Error('deal above the mandate limit: ' + nd.mission.negotiation.agreed_price)

// spending insights: charts and the purchased-items grid
await page.goto(base + '/#/insights')
await page.waitForSelector('text=Spending insights')
await page.waitForSelector('.ag-row', { timeout: 10000 })
await page.waitForTimeout(1200)
await page.screenshot({ path: `${S}/09-insights.png`, fullPage: true })

await page.goto(base + '/#/mandates/new')
await page.waitForSelector('text=New mandate')
await page.screenshot({ path: `${S}/07-new-mandate.png`, fullPage: true })

// dark mode dashboard
const dark = await browser.newPage({ viewport: { width: 1360, height: 900 }, colorScheme: 'dark' })
await dark.goto(base + '/#/')
await dark.waitForSelector('text=Desk refresh')
await dark.screenshot({ path: `${S}/08-dashboard-dark.png`, fullPage: true })
await browser.close()
if (pageErrors) throw new Error(`${pageErrors} page error(s) during the walk-through`)
console.log('SMOKE_OK')
})().catch(e => { console.error("SMOKE_FAIL", e); process.exit(1) })
