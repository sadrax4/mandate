import { Fragment, type ReactNode } from 'react'

/** Inline formatting: **bold**, `code`, *italic*. Input is plain text, never HTML. */
function inline(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = []
  const re = /(\*\*[^*]+\*\*|`[^`]+`|\*[^*\n]+\*)/g
  let last = 0
  let m: RegExpExecArray | null
  let i = 0
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const tok = m[0]
    if (tok.startsWith('**')) out.push(<strong key={`${key}-b${i}`}>{tok.slice(2, -2)}</strong>)
    else if (tok.startsWith('`')) out.push(<code key={`${key}-c${i}`}>{tok.slice(1, -1)}</code>)
    else out.push(<em key={`${key}-i${i}`}>{tok.slice(1, -1)}</em>)
    last = m.index + tok.length
    i++
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

/**
 * Markdown renders the small subset of Markdown the agent produces (headings,
 * bullet and numbered lists, paragraphs, bold/italic/code) as React elements.
 * It never injects HTML, so model output cannot script the page.
 */
export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r/g, '').split('\n')
  const blocks: ReactNode[] = []
  let para: string[] = []
  let list: { ordered: boolean; items: string[] } | null = null
  let k = 0
  const flushPara = () => {
    if (para.length) {
      blocks.push(<p key={`p${k++}`}>{inline(para.join(' '), `p${k}`)}</p>)
      para = []
    }
  }
  const flushList = () => {
    if (list) {
      const items = list.items.map((it, i) => <li key={i}>{inline(it, `l${k}-${i}`)}</li>)
      blocks.push(list.ordered ? <ol key={`ol${k++}`}>{items}</ol> : <ul key={`ul${k++}`}>{items}</ul>)
      list = null
    }
  }
  for (const raw of lines) {
    const line = raw.trim()
    const h = /^(#{1,6})\s+(.*)$/.exec(line)
    const bullet = /^[-*•]\s+(.*)$/.exec(line)
    const num = /^\d+[.)]\s+(.*)$/.exec(line)
    if (line === '') {
      flushPara()
      flushList()
    } else if (h) {
      flushPara()
      flushList()
      blocks.push(<h4 key={`h${k++}`}>{inline(h[2], `h${k}`)}</h4>)
    } else if (bullet || num) {
      flushPara()
      const ordered = !!num
      const item = (bullet ?? num)![1]
      if (!list || list.ordered !== ordered) {
        flushList()
        list = { ordered, items: [] }
      }
      list.items.push(item)
    } else {
      if (list) flushList()
      para.push(line)
    }
  }
  flushPara()
  flushList()
  return <Fragment>{blocks}</Fragment>
}
