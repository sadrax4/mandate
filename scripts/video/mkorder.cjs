// node mkorder.cjs <scenesDir> <scene,scene,...> > order.json
const [dir, list] = process.argv.slice(2)
const css = `html,body{margin:0;width:1920px;height:1080px;font-family:Inter,"Liberation Sans",Arial,sans-serif;background:#0f1a1a;color:#fff}
.w{width:1920px;height:1080px;display:flex;flex-direction:column;justify-content:center;align-items:center;text-align:center;background:radial-gradient(1200px 700px at 50% 38%,#17302e 0%,#0f1a1a 70%)}
h1{font-size:132px;letter-spacing:-3px;margin:0;font-weight:800}
p{font-size:44px;line-height:1.3;margin:28px 0 0;color:#cfe3e0;max-width:1400px}
.k{font-size:30px;letter-spacing:6px;text-transform:uppercase;color:#5fd1c4;margin-bottom:28px}
.u{font-size:40px;margin-top:16px;color:#fff;font-weight:600}
.s{font-size:30px;color:#9fbab6;margin-top:36px}`
const title = { type: 'card', name: 'title', seconds: 4, html: `<style>${css}</style><div class="w"><div class="k">PayPal AI Hackathon</div><h1>Mandate</h1><p>Give an AI agent a budget.<br>Keep the final say.</p></div>`, caps: [{ start: 0.6, end: 3.6, text: 'Mandate: give an AI agent a budget. Keep the final say.' }] }
const end = { type: 'card', name: 'end', seconds: 5, html: `<style>${css}</style><div class="w"><div class="k">The agent proposes. You approve. PayPal pays.</div><h1 style="font-size:96px">Mandate</h1><div class="u">github.com/sadrax4/mandate</div><div class="u">Live demo: 23.27.20.150:8080</div><div class="s">Go · React · AG Grid · PayPal Orders, Vault and MCP · Claude · Channel3</div></div>`, caps: [] }
const scenes = list.split(',').map((n) => ({ type: 'scene', name: n, dir: `${dir}/${n}` }))
console.log(JSON.stringify([title, ...scenes, end]))
