// The scenes of the Mandate demo video. Usage:
//   BASE_URL=http://localhost:8099 MODE=mock node scenes.cjs intro mandate buy ...
//   BASE_URL=http://23.27.20.150:8080 MODE=live PP_EMAIL=... PP_PASS=... node scenes.cjs buy
// MODE=live drives the real PayPal sandbox checkout; MODE=mock uses the built-in simulator.
const L = require('./lib.cjs')
const { click, type, sleep } = L
const fs = require('fs')

const BASE = process.env.BASE_URL || 'http://localhost:8099'
const MODE = process.env.MODE || 'mock'
const OUT = process.env.OUT || 'scenes'
const LIVE = MODE === 'live'
const PROXY = process.env.PROXY ? { server: process.env.PROXY } : undefined
const BADGE = LIVE ? '' : 'OFFLINE DEMO MODE'
const WAIT = LIVE ? 150000 : 60000

const goalBox = (page) => page.locator('#goal-input')
const startBtn = (page) => page.getByRole('button', { name: 'Start mission' })

async function useMandate(page, name) {
  const sel = page.locator('select[aria-label="Mandate for the next mission"]')
  await sel.waitFor({ timeout: 15000 })
  const value = await sel.evaluate((s, n) => [...s.options].find((o) => o.text.startsWith(n))?.value, name)
  if (!value) throw new Error('no mandate named ' + name)
  const c = await L.center(page, sel)
  await L.glide(page, c.x, c.y, 600)
  await sel.selectOption(value)
  await sleep(500)
}

/** The PayPal sandbox checkout, driven like a person would (LIVE), or the built-in simulator (mock). */
async function payAtPayPal(page, rec) {
  if (!LIVE) {
    await page.waitForSelector('text=Pay with PayPal (simulated)', { timeout: 20000 })
    rec.cap('The checkout is simulated in offline mode.\nOn the live demo this is PayPal\'s own sandbox page.')
    await sleep(2500)
    await click(page, page.getByRole('button', { name: 'Approve and pay' }))
    return
  }
  const email = process.env.PP_EMAIL, pass = process.env.PP_PASS
  if (!email || !pass) throw new Error('set PP_EMAIL and PP_PASS (the sandbox buyer from the README)')
  await rec.lapse('Mandate sends me to PayPal\'s own sandbox checkout.\nNo real money moves.', 3.5, async () => {
    await page.waitForURL(/paypal\.com/, { timeout: 60000 })
    await page.locator('input#email, input[name="login_email"], input[type="email"]').first().waitFor({ timeout: 60000 })
  })
  const emailBox = page.locator('input#email, input[name="login_email"], input[type="email"]').first()
  await sleep(600)
  rec.cap('I log in as a PayPal sandbox test buyer.')
  await type(page, emailBox, email, { delay: 45 })
  const next = page.locator('#btnNext, button:has-text("Next")').first()
  if (await next.isVisible().catch(() => false)) await click(page, next)
  const pw = page.locator('input#password, input[name="login_password"], input[type="password"]').first()
  await pw.waitFor({ timeout: 30000 })
  await type(page, pw, pass, { delay: 60 })
  await click(page, page.locator('#btnLogin, button:has-text("Log In")').first())
  const pay = page.locator('#payment-submit-btn, button:has-text("Complete Purchase"), button:has-text("Pay Now"), button:has-text("Agree & Pay"), button:has-text("Continue")').first()
  await rec.lapse('Logged in. PayPal shows what I am paying for.', 3, async () => { await pay.waitFor({ timeout: 60000 }) })
  await sleep(2000)
  await click(page, pay)
}

const SCENES = {
  // 1. the app, and what it is
  async intro(page, rec) {
    await page.goto(BASE + '/#/')
    await page.waitForSelector('.topbar')
    await sleep(600)
    rec.cap(LIVE ? 'This is the real app, running on my server.' : 'This is the app in offline demo mode.')
    await sleep(3200)
    if (LIVE) {
      await page.evaluate(() => { const e = document.querySelector('.topbar .modepill'); if (e) { e.style.outline = '3px solid #f59e0b'; e.style.outlineOffset = '6px'; e.style.borderRadius = '8px' } })
      rec.cap('Running for real: PayPal sandbox, Claude,\na live retail catalog and PayPal\'s MCP server.')
      await sleep(4200)
    } else {
      rec.cap('The agent researches and proposes.\nOnly you and PayPal move money.')
      await sleep(3600)
    }
  },

  // 2. the authority you grant
  async mandate(page, rec) {
    await page.goto(BASE + '/#/')
    await page.waitForSelector('.topbar')
    await sleep(500)
    rec.cap('A mandate is the authority you grant:\na budget, a cap per item, and when it may pay on its own.')
    await click(page, page.getByRole('link', { name: 'New mandate' }).first())
    await page.waitForSelector('text=Total budget')
    const field = (label) => page.locator('label.field', { hasText: label }).locator('input')
    await type(page, page.locator('input[placeholder="e.g. Home office refresh"]'), 'Home office')
    for (const [label, val] of [['Total budget', '300'], ['Max per item', '150'], ['Auto-approve carts under', '40']]) {
      const f = field(label)
      await click(page, f, { after: 100 })
      await f.press('Control+A')
      await f.pressSequentially(val, { delay: 70 })
      await sleep(350)
    }
    rec.cap('Under $40 it can pay by itself, with the PayPal\naccount I save at the first checkout.')
    await sleep(2600)
    await click(page, page.getByRole('button', { name: 'Create mandate' }))
    await page.waitForSelector('.mandate-card .name:has-text("Home office")')
    await sleep(1800)
  },

  // 3. a goal, a proposal, a human decision, a real PayPal payment
  async buy(page, rec) {
    await page.goto(BASE + '/#/')
    await page.waitForSelector('#goal-input')
    await sleep(500)
    rec.cap('I give the agent a goal in plain language.')
    await useMandate(page, 'Home office')
    await type(page, goalBox(page), 'a mechanical keyboard with brown switches under $120')
    await click(page, startBtn(page))
    await page.waitForURL(/#\/missions\//)
    await rec.lapse('Claude searches the catalog, compares products and checks\nevery cart against the mandate (sped up).', 5, async () => {
      await page.waitForSelector('text=The agent proposes', { timeout: WAIT })
    })
    await sleep(800)
    rec.cap('The agent can propose. It cannot pay.\nThe cart passed every rule in the mandate.')
    await L.scrollBy(page, 140, 800)
    await sleep(3800)
    rec.cap('Why this cart, and everything it looked at:\nthe story is right here.')
    await L.scrollBy(page, 420, 1000)
    await sleep(3200)
    await L.scrollTo(page, 0, 900)
    rec.cap('I approve. Mandate creates a PayPal order\nfor exactly this cart.')
    await click(page, page.getByRole('button', { name: /Approve & pay/ }))
    await payAtPayPal(page, rec)
    await rec.lapse('Back in Mandate: the server captures the payment.', LIVE ? 3 : 1, async () => {
      await page.waitForSelector('text=Paid through PayPal', { timeout: 90000 })
    })
    rec.cap('Captured through PayPal. The receipt carries the\norder and capture IDs, and the budget updates.')
    await sleep(2200)
    await L.scrollBy(page, 330, 900)
    await sleep(3600)
  },

  // 4. the saved PayPal account pays inside the limit, with no click
  async autopay(page, rec) {
    await page.goto(BASE + '/#/')
    await page.waitForSelector('#goal-input')
    await useMandate(page, 'Home office')
    rec.cap('Next goal. This one costs less than my $40 limit.')
    await type(page, goalBox(page), 'a USB-C hub with HDMI under $35')
    await click(page, startBtn(page))
    await page.waitForURL(/#\/missions\//)
    await rec.lapse('Claude researches. The mandate checks the cart\nand the saved PayPal account pays it (sped up).', 5, async () => {
      await page.waitForSelector('text=/Paid through PayPal|The agent proposes/', { timeout: WAIT })
      await sleep(1500)
    })
    const paid = await page.locator('text=Paid through PayPal').count()
    rec.cap(paid ? 'Under my limit, so it paid itself:\nno click, and the mandate still decided every cent.' : 'This cart needs my approval.')
    await sleep(4200)
  },

  // 5. a human says no, with a reason
  async reject(page, rec) {
    await page.goto(BASE + '/#/')
    await page.waitForSelector('#goal-input')
    await useMandate(page, 'Home office')
    rec.cap('Above my auto-pay limit, the agent has to ask.')
    await type(page, goalBox(page), 'noise cancelling headphones under $150')
    await click(page, startBtn(page))
    await page.waitForURL(/#\/missions\//)
    await rec.lapse('Claude researches and proposes (sped up).', 4, async () => {
      await page.waitForSelector('text=The agent proposes', { timeout: WAIT })
    })
    await sleep(1800)
    rec.cap('Too expensive? I send it back, with a reason.')
    await click(page, page.getByRole('button', { name: /Reject/ }).first())
    await type(page, page.locator('#reject-reason'), 'too expensive, I want something under $60')
    await sleep(500)
    await click(page, page.getByRole('button', { name: 'Send back to the agent' }))
    await rec.lapse('The agent revises in the same conversation (sped up).', 4, async () => {
      await page.waitForSelector('text=/after your feedback|Paid through PayPal/', { timeout: WAIT })
      await sleep(1500)
    })
    rec.cap('A new cart that respects what I said.')
    await sleep(3800)
  },

  // 6. two agents haggle; the human decides
  async negotiate(page, rec) {
    await page.goto(BASE + '/#/')
    await page.waitForSelector('#goal-input')
    await useMandate(page, 'Home office')
    rec.cap('Services work too: two agents negotiate the price.')
    const kind = page.locator('select[aria-label="Mission kind"]')
    const c = await L.center(page, kind)
    await L.glide(page, c.x, c.y, 600)
    await kind.selectOption('negotiate')
    await sleep(400)
    await type(page, goalBox(page), 'a two-hour product photo shoot for 20 items')
    await click(page, startBtn(page))
    await page.waitForURL(/#\/missions\//)
    await rec.lapse('My buyer agent haggles with a seller agent.\nIt can never go above the limit I set (sped up).', 6, async () => {
      await page.waitForSelector('text=below asking', { timeout: WAIT })
    })
    await sleep(800)
    rec.cap('The agreed price becomes an ordinary proposal.\nNeither agent can pay. I decide.')
    await L.scrollBy(page, 520, 1200)
    await sleep(4200)
  },

  // 7. the ledger, and PayPal's own MCP server answering questions about it
  async ledger(page, rec) {
    await page.goto(BASE + '/#/ledger')
    await page.waitForSelector('.ag-row', { timeout: 20000 })
    await sleep(600)
    rec.cap('Every order and capture lands in the ledger.')
    await sleep(3200)
    const ask = page.locator('input[aria-label="Question for the order assistant"]')
    if (await ask.count()) {
      await L.scrollBy(page, 360, 900)
      rec.cap('An assistant reads PayPal itself through PayPal\'s\nMCP server. It can look, never pay.')
      await type(page, ask, 'Did my most recent order get captured? Confirm it with PayPal.', { delay: 30 })
      await click(page, page.getByRole('button', { name: 'Ask', exact: true }))
      await rec.lapse('Asking PayPal (sped up).', 4, async () => {
        await page.waitForSelector('.chip:has-text("PayPal MCP")', { timeout: 90000 })
      })
      await L.scrollBy(page, 260, 800)
      await sleep(3800)
    }
  },

  async insights(page, rec) {
    await page.goto(BASE + '/#/insights')
    await page.waitForSelector('.ag-row', { timeout: 20000 })
    await sleep(900)
    rec.cap('Insights: where the money went, by category,\nmerchant and day.')
    await sleep(3600)
    await L.scrollBy(page, 560, 1000)
    await sleep(2400)
  },
}

async function runScene(name) {
  const fn = SCENES[name]
  if (!fn) throw new Error('unknown scene ' + name + '; have ' + Object.keys(SCENES).join(', '))
  const dark = name === 'insights' && process.env.DARK === '1'
  const { browser, page } = await L.launch({ proxy: PROXY, dark })
  const rec = new L.Recorder(page, `${OUT}/${name}`)
  try {
    await page.goto(BASE + '/#/')
    await page.waitForSelector('.topbar', { timeout: 30000 })
    await rec.start()
    await fn(page, rec)
    const meta = await rec.stop({ badge: BADGE })
    console.log(`scene ${name}: ${meta.frames.length} frames, ${(meta.t1 - meta.t0).toFixed(1)}s recorded`)
  } catch (e) {
    await page.screenshot({ path: `${OUT}/FAIL-${name}.png` }).catch(() => {})
    console.error(`scene ${name} FAILED:`, e.message)
    console.error('screenshot:', `${OUT}/FAIL-${name}.png`, 'url:', page.url())
    await rec.stop({ badge: BADGE, failed: true }).catch(() => {})
    process.exitCode = 1
    throw e
  } finally {
    await browser.close()
  }
}

;(async () => {
  fs.mkdirSync(OUT, { recursive: true })
  const names = process.argv.slice(2)
  for (const n of names) await runScene(n)
})().catch(() => process.exit(1))
