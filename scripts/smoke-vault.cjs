// Browser check for saved-PayPal (vault) payments in offline demo mode.
// Run the server as for scripts/smoke.cjs, then:
//   BASE_URL=http://localhost:8080 SHOTS=/tmp/shots node scripts/smoke-vault.cjs
const { chromium } = require('playwright'); (async () => {
const base = process.env.BASE_URL || 'http://localhost:8080'
const S = process.env.SHOTS || 'docs/screenshots'
const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1360, height: 900 } })
page.on('pageerror', (e) => console.log('PAGEERROR', e.message))
page.on('console', (m) => { if (m.type() === 'error') console.log('CONSOLE', m.text()) })

const md = await (await page.request.post(base + '/api/mandates', { data: { name: 'Vault demo', currency: 'USD', total_budget: 1000, auto_approve_under: 60, save_payment_method: true } })).json()
if (md.vault_id) throw new Error('vault_id must start empty')

// 1. first purchase: under the threshold, so it goes straight to (simulated) checkout and saves the account
const m1 = await (await page.request.post(base + '/api/missions', { data: { mandate_id: md.id, goal: 'aeropress coffee maker' } })).json()
await page.goto(base + '/#/missions/' + m1.id)
await page.waitForSelector('text=Continue to PayPal checkout', { timeout: 20000 })
await page.click('a:has-text("Continue to PayPal checkout")')
await page.waitForSelector('text=Pay with PayPal (simulated)', { timeout: 10000 })
await page.click('button:has-text("Approve and pay")')
await page.waitForSelector('text=Payment captured', { timeout: 10000 })
await page.waitForSelector('text=PayPal account saved on this mandate', { timeout: 10000 })
await page.waitForTimeout(500)
await page.screenshot({ path: `${S}/09-vault-saved.png`, fullPage: true })

await page.goto(base + '/#/')
await page.waitForSelector('text=PayPal saved · one-click approvals, auto-pay under $60.00', { timeout: 10000 })

// 2. a cart above the threshold: human approves, paid with the saved account, no redirect
const m2 = await (await page.request.post(base + '/api/missions', { data: { mandate_id: md.id, goal: 'noise cancelling headphones' } })).json()
await page.goto(base + '/#/missions/' + m2.id)
await page.waitForSelector('button:has-text("Approve & pay with saved PayPal")', { timeout: 20000 })
await page.click('button:has-text("Approve & pay with saved PayPal")')
await page.waitForSelector('text=none: paid with the PayPal account saved on this mandate', { timeout: 10000 })
if (!page.url().includes('/#/missions/' + m2.id)) throw new Error('should not have left the mission page: ' + page.url())
await page.waitForSelector('text=no checkout needed', { timeout: 5000 })
await page.waitForTimeout(500)
await page.screenshot({ path: `${S}/10-one-click-paid.png`, fullPage: true })

// 3. a cart under the threshold: paid while the agent is still running
const m3 = await (await page.request.post(base + '/api/missions', { data: { mandate_id: md.id, goal: 'aeropress coffee maker' } })).json()
await page.goto(base + '/#/missions/' + m3.id)
await page.waitForSelector('text=none: paid with the PayPal account saved on this mandate', { timeout: 20000 })
const detail = await (await page.request.get(base + '/api/missions/' + m3.id)).json()
if (detail.mission.status !== 'paid' || !detail.payments[0].vaulted || detail.payments[0].approve_url) throw new Error('auto-pay failed: ' + JSON.stringify(detail.payments))

await page.goto(base + '/#/ledger')
await page.waitForSelector('.ag-row', { timeout: 10000 })
const ledger = await (await page.request.get(base + '/api/ledger')).json()
if (!ledger.some((r) => r.vaulted && r.status === 'COMPLETED')) throw new Error('ledger has no saved-PayPal payment')
await browser.close()
console.log('SMOKE_VAULT_OK')
})().catch(e => { console.error('SMOKE_VAULT_FAIL', e); process.exit(1) })
