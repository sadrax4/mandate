// Recording helpers for the Mandate demo video.
//  - frames come straight from Chromium's screencast (crisp, 1920x1080), with exact timestamps;
//  - a visible cursor glides to every click, typing is human-paced;
//  - captions and "time-lapse" ranges are marked while recording and applied by build.cjs.
const { chromium } = require('playwright')
const fs = require('fs')
const path = require('path')

const VIEW = { width: 1280, height: 720 }
const DSF = 1.5 // 1280x720 CSS pixels -> 1920x1080 frames
const now = () => Date.now() / 1000
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const CURSOR_JS = `(() => {
  if (window.__cursorInstalled) return
  window.__cursorInstalled = true
  const install = () => {
    if (document.getElementById('__cur')) return
    const st = document.createElement('style')
    st.textContent = '#__cur{position:fixed;left:0;top:0;z-index:2147483647;pointer-events:none;width:26px;height:26px;margin:-3px 0 0 -3px;transition:none;display:none}' +
      '.__rip{position:fixed;z-index:2147483646;pointer-events:none;width:14px;height:14px;margin:-7px 0 0 -7px;border-radius:50%;border:3px solid rgba(15,118,110,.9);animation:__rip .5s ease-out forwards}' +
      '@keyframes __rip{from{transform:scale(.4);opacity:1}to{transform:scale(3.2);opacity:0}}'
    document.documentElement.appendChild(st)
    const c = document.createElement('div')
    c.id = '__cur'
    c.innerHTML = '<svg width="26" height="26" viewBox="0 0 26 26"><path d="M3 2l17 8.5-7 2.2-2.6 7z" fill="#fff" stroke="#111" stroke-width="1.6" stroke-linejoin="round"/></svg>'
    document.documentElement.appendChild(c)
    window.addEventListener('mousemove', (e) => { c.style.display = 'block'; c.style.transform = 'translate(' + e.clientX + 'px,' + e.clientY + 'px)' }, true)
    window.addEventListener('mousedown', (e) => {
      const r = document.createElement('div'); r.className = '__rip'; r.style.left = e.clientX + 'px'; r.style.top = e.clientY + 'px'
      document.documentElement.appendChild(r); setTimeout(() => r.remove(), 600)
    }, true)
  }
  if (document.documentElement) install(); else document.addEventListener('DOMContentLoaded', install)
  document.addEventListener('DOMContentLoaded', install)
})()`

async function launch({ dark = false, proxy, headless = true, viewport = VIEW, dsf = DSF } = {}) {
  const browser = await chromium.launch({
    headless,
    args: ['--hide-scrollbars', '--disable-blink-features=AutomationControlled', '--autoplay-policy=no-user-gesture-required'],
    ...(proxy ? { proxy } : {}),
  })
  const ctx = await browser.newContext({
    viewport,
    deviceScaleFactor: dsf,
    colorScheme: dark ? 'dark' : 'light',
    locale: 'en-US',
    timezoneId: 'Europe/London',
    userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36',
  })
  await ctx.addInitScript(CURSOR_JS)
  const page = await ctx.newPage()
  return { browser, ctx, page }
}

class Recorder {
  constructor(page, dir) {
    this.page = page
    this.dir = dir
    this.frames = []
    this.caps = []
    this.segs = []
    this.n = 0
    fs.rmSync(dir, { recursive: true, force: true })
    fs.mkdirSync(dir, { recursive: true })
  }
  async start() {
    this.cdp = await this.page.context().newCDPSession(this.page)
    this.cdp.on('Page.screencastFrame', (f) => {
      const file = `f${String(this.n++).padStart(6, '0')}.jpg`
      fs.writeFileSync(path.join(this.dir, file), Buffer.from(f.data, 'base64'))
      this.frames.push({ t: f.metadata.timestamp, file })
      this.cdp.send('Page.screencastFrameAck', { sessionId: f.sessionId }).catch(() => {})
    })
    await this.cdp.send('Page.startScreencast', { format: 'jpeg', quality: 92, maxWidth: 1920, maxHeight: 1080, everyNthFrame: 1 })
    this.t0 = now()
    // make sure there is a first frame even on a static page
    await this.page.evaluate(() => { document.documentElement.style.outline = '0.001px solid transparent' }).catch(() => {})
    await sleep(250)
  }
  /** Show a caption from now until the next cap()/capOff(). */
  cap(text) { this.caps.push({ t: now(), text }) }
  capOff() { this.caps.push({ t: now(), text: '' }) }
  /** Run fn (a wait); the waited stretch plays back in about outSec seconds, captioned. */
  async lapse(text, outSec, fn) {
    const a = now()
    this.cap(text)
    await fn()
    let b = now()
    if (b - a < outSec) { await sleep((outSec - (b - a)) * 1000); b = now() }
    this.segs.push({ a, b, speed: Math.max(1, (b - a) / outSec) })
  }
  async stop(meta = {}) {
    await sleep(400)
    const t1 = now()
    await this.cdp.send('Page.stopScreencast').catch(() => {})
    await sleep(150)
    const out = { t0: this.t0, t1, frames: this.frames, caps: this.caps, segs: this.segs, ...meta }
    fs.writeFileSync(path.join(this.dir, 'meta.json'), JSON.stringify(out))
    return out
  }
}

// ---- human-paced input ----
const ease = (x) => (x < 0.5 ? 2 * x * x : 1 - Math.pow(-2 * x + 2, 2) / 2)
let mouse = { x: 640, y: 360 }

async function glide(page, x, y, ms = 650) {
  const steps = Math.max(8, Math.round(ms / 16))
  const from = { ...mouse }
  for (let i = 1; i <= steps; i++) {
    const k = ease(i / steps)
    await page.mouse.move(from.x + (x - from.x) * k, from.y + (y - from.y) * k)
    await sleep(ms / steps)
  }
  mouse = { x, y }
}
async function center(page, loc) {
  await loc.scrollIntoViewIfNeeded().catch(() => {})
  const b = await loc.boundingBox()
  if (!b) throw new Error('element not visible: ' + loc)
  return { x: b.x + b.width / 2, y: b.y + b.height / 2, b }
}
async function click(page, loc, { ms = 650, after = 350, dx = 0, dy = 0 } = {}) {
  const { x, y } = await center(page, loc)
  await glide(page, x + dx, y + dy, ms)
  await sleep(120)
  await page.mouse.down(); await sleep(60); await page.mouse.up()
  await sleep(after)
}
async function type(page, loc, text, { delay = 38 } = {}) {
  await click(page, loc, { after: 150 })
  await loc.pressSequentially(text, { delay })
  await sleep(250)
}
async function scrollTo(page, y, ms = 900) {
  await page.evaluate((top) => window.scrollTo({ top, behavior: 'smooth' }), y)
  await sleep(ms)
}
async function scrollBy(page, dy, ms = 900) {
  await page.evaluate((d) => window.scrollBy({ top: d, behavior: 'smooth' }), dy)
  await sleep(ms)
}

module.exports = { launch, Recorder, glide, click, type, scrollTo, scrollBy, sleep, now, center, VIEW, DSF }
