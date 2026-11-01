// Turns recorded scene folders into captioned clips, adds title/end cards and joins everything.
//   node build.cjs <order.json> <outdir>
// order.json: [ {"type":"card","name":"title","seconds":4,"html":"..."}, {"type":"scene","dir":"scenes/s1"}, ... ]
const fs = require('fs'), path = require('path')
const { execFileSync } = require('child_process')
const FONT = process.env.CAPTION_FONT || 'Liberation Sans'
const W = 1920, H = 1080, FPS = 30

const ff = (args) => execFileSync('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', ...args], { stdio: ['ignore', 'inherit', 'inherit'] })

function mapper(meta) {
  const segs = [...meta.segs].sort((x, y) => x.a - y.a)
  return (t) => {
    let out = t - meta.t0
    for (const s of segs) out -= Math.min(Math.max(t - s.a, 0), s.b - s.a) * (1 - 1 / s.speed)
    return Math.max(0, out)
  }
}
const ts = (s) => { // ASS time h:mm:ss.cc
  const cs = Math.round(s * 100), h = Math.floor(cs / 360000), m = Math.floor((cs % 360000) / 6000), sec = Math.floor((cs % 6000) / 100)
  return `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}.${String(cs % 100).padStart(2, '0')}`
}
const srtTs = (s) => {
  const ms = Math.round(s * 1000), h = Math.floor(ms / 3600000), m = Math.floor((ms % 3600000) / 60000), sec = Math.floor((ms % 60000) / 1000)
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')},${String(ms % 1000).padStart(3, '0')}`
}

function ass(caps, badge) {
  const head = `[Script Info]
ScriptType: v4.00+
PlayResX: ${W}
PlayResY: ${H}
WrapStyle: 0
ScaledBorderAndShadow: yes

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Cap,${FONT},44,&H00FFFFFF,&H000000FF,&H26141414,&H26141414,-1,0,0,0,100,100,0,0,3,16,0,2,200,200,52,1
Style: Badge,${FONT},26,&H00FFFFFF,&H000000FF,&H200953B4,&H200953B4,-1,0,0,0,100,100,1,0,3,10,0,8,40,40,22,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`
  const esc = (t) => t.replace(/\\/g, '').replace(/[{}]/g, '').replace(/\n/g, '\\N')
  let body = ''
  for (const c of caps) body += `Dialogue: 0,${ts(c.start)},${ts(c.end)},Cap,,0,0,0,,${esc(c.text)}\n`
  if (badge) body += `Dialogue: 1,${ts(0)},${ts(9999)},Badge,,0,0,0,,${esc(badge)}\n`
  return head + body
}

/** One recorded scene -> captioned mp4. Returns { file, dur, caps } (caps in clip time). */
function buildScene(dir, out) {
  const meta = JSON.parse(fs.readFileSync(path.join(dir, 'meta.json'), 'utf8'))
  const map = mapper(meta)
  const frames = meta.frames.filter((f) => f.t >= meta.t0 - 0.05 || true)
  // frame start times in clip time; the first frame covers the very start
  const starts = frames.map((f, i) => (i === 0 ? 0 : map(f.t)))
  const end = map(meta.t1)
  const lines = ['ffconcat version 1.0']
  let kept = []
  for (let i = 0; i < frames.length; i++) {
    const nextStart = i + 1 < frames.length ? starts[i + 1] : end
    const dur = nextStart - starts[i]
    if (dur < 0.004 && i + 1 < frames.length) continue
    kept.push({ file: frames[i].file, dur: Math.max(dur, 0.004) })
  }
  for (const k of kept) lines.push(`file '${path.resolve(dir, k.file)}'`, `duration ${k.dur.toFixed(4)}`)
  lines.push(`file '${path.resolve(dir, kept[kept.length - 1].file)}'`)
  fs.writeFileSync(path.join(dir, 'list.txt'), lines.join('\n'))

  // captions -> clip time; a caption lasts until the next one (or the end of the clip)
  const caps = []
  const ev = meta.caps.slice().sort((a, b) => a.t - b.t)
  for (let i = 0; i < ev.length; i++) {
    if (!ev[i].text) continue
    const start = map(ev[i].t), stop = i + 1 < ev.length ? map(ev[i + 1].t) : end
    if (stop - start > 0.2) caps.push({ start, end: stop, text: ev[i].text })
  }
  const assFile = path.join(dir, 'captions.ass')
  fs.writeFileSync(assFile, ass(caps, meta.badge))
  const vf = `fps=${FPS},scale=${W}:${H}:flags=lanczos,subtitles=${assFile.replace(/\\/g, '/').replace(/:/g, '\\:')},format=yuv420p`
  ff(['-f', 'concat', '-safe', '0', '-i', path.join(dir, 'list.txt'), '-vf', vf, '-r', String(FPS), '-c:v', 'libx264', '-preset', process.env.X264_PRESET || 'veryfast', '-crf', '21', '-an', out])
  if (process.env.PRUNE === '1') for (const f of fs.readdirSync(dir)) if (f.endsWith('.jpg')) fs.rmSync(path.join(dir, f))
  return { file: out, dur: end, caps }
}

async function buildCard(card, out) {
  const { chromium } = require('playwright')
  const b = await chromium.launch(); const p = await b.newPage({ viewport: { width: W, height: H }, deviceScaleFactor: 1 })
  await p.setContent(card.html, { waitUntil: 'load' })
  const png = out.replace(/\.mp4$/, '.png')
  await p.screenshot({ path: png }); await b.close()
  const s = card.seconds
  ff(['-loop', '1', '-framerate', String(FPS), '-t', String(s), '-i', png, '-vf', `fade=in:0:12,fade=out:st=${(s - 0.5).toFixed(2)}:d=0.5,format=yuv420p`, '-r', String(FPS), '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '21', '-an', out])
  return { file: out, dur: s, caps: card.caps || [] }
}

async function main() {
  const [orderFile, outDir] = process.argv.slice(2)
  const order = JSON.parse(fs.readFileSync(orderFile, 'utf8'))
  fs.mkdirSync(outDir, { recursive: true })
  const clips = []
  for (const [i, o] of order.entries()) {
    const out = path.join(outDir, `${String(i).padStart(2, '0')}-${o.name || path.basename(o.dir)}.mp4`)
    clips.push(o.type === 'card' ? await buildCard(o, out) : buildScene(o.dir, out))
    console.log('built', path.basename(out), clips[clips.length - 1].dur.toFixed(1) + 's')
  }
  fs.writeFileSync(path.join(outDir, 'join.txt'), clips.map((c) => `file '${path.resolve(c.file)}'`).join('\n'))
  const final = path.join(outDir, 'mandate-demo.mp4')
  ff(['-f', 'concat', '-safe', '0', '-i', path.join(outDir, 'join.txt'), '-c', 'copy', '-movflags', '+faststart', final])
  // SRT with the same captions (for YouTube; do not enable both burned-in and uploaded captions)
  let off = 0, n = 1, srt = ''
  for (const c of clips) {
    for (const cap of c.caps) srt += `${n++}\n${srtTs(off + cap.start)} --> ${srtTs(off + cap.end)}\n${cap.text}\n\n`
    off += c.dur
  }
  fs.writeFileSync(path.join(outDir, 'mandate-demo.srt'), srt)
  console.log('TOTAL', off.toFixed(1) + 's', '->', final)
  if (off >= 180) console.log('WARNING: the video is 3:00 or longer; Devpost judges stop at 3:00 and the rules say "less than three minutes"')
}
main().catch((e) => { console.error(e); process.exit(1) })
