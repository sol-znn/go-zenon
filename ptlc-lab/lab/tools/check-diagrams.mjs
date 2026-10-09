#!/usr/bin/env node
// Renders every Mermaid block in this repository's Markdown and says whether
// each one drew.
//
//   node lab/tools/check-diagrams.mjs [file.md ...]
//
// `mermaid.parse()` checks grammar only: a malformed colour or a shape that
// does not exist parses cleanly and then draws an error box where the diagram
// should be. So each block is rendered, in headless Chrome with the mermaid
// build the lab page ships, and the SVG is read: it must not be Mermaid's
// error diagram, and it must contain the participants or nodes the source
// names. Needs `npm install` in lab/web, and Chrome (CHROME_PATH to say where).

import { spawn } from 'node:child_process'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const mermaidDist = path.join(root, 'lab', 'web', 'node_modules', 'mermaid', 'dist')

function markdownFiles(dir, found = []) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === '.git' || name === 'dist') continue
    const full = path.join(dir, name)
    if (statSync(full).isDirectory()) markdownFiles(full, found)
    else if (name.endsWith('.md')) found.push(full)
  }
  return found
}

const files = process.argv.length > 2 ? process.argv.slice(2).map((f) => path.resolve(f)) : markdownFiles(root)
const diagrams = []
for (const file of files) {
  const text = readFileSync(file, 'utf8')
  const pattern = /```mermaid\r?\n([\s\S]*?)```/g
  let match
  while ((match = pattern.exec(text)) !== null) {
    const line = text.slice(0, match.index).split('\n').length
    diagrams.push({ file: path.relative(root, file).replace(/\\/g, '/'), line, code: match[1] })
  }
}
if (diagrams.length === 0) {
  console.log('No Mermaid blocks found.')
  process.exit(0)
}
if (!existsSync(path.join(mermaidDist, 'mermaid.esm.min.mjs'))) {
  console.error('mermaid is not installed: run `npm install` in lab/web first.')
  process.exit(2)
}

const html = `<!doctype html><meta charset="utf-8"><body><pre id="result">pending</pre>
<script type="module">
import mermaid from '/mermaid/mermaid.esm.min.mjs'
const diagrams = ${JSON.stringify(diagrams).replace(/</g, '\\u003c')}
mermaid.initialize({ startOnLoad: false, securityLevel: 'strict' })
const out = []
for (let i = 0; i < diagrams.length; i++) {
  const d = diagrams[i]
  const row = { file: d.file, line: d.line, kind: d.code.trim().split(/\\s+/)[0] }
  try {
    const { svg } = await mermaid.render('d' + i, d.code)
    const doc = new DOMParser().parseFromString(svg, 'image/svg+xml')
    row.error = svg.includes('aria-roledescription="error"')
    row.actors = doc.querySelectorAll('rect.actor-top, .actor-top').length
    row.nodes = doc.querySelectorAll('g.node, g.stateGroup, g.statediagram-state').length
    row.messages = doc.querySelectorAll('.messageText').length
  } catch (err) {
    row.error = true
    row.message = String(err && err.message || err).split('\\n')[0]
  }
  out.push(row)
}
document.getElementById('result').textContent = 'DONE' + JSON.stringify(out)
</script>`

const types = { '.mjs': 'text/javascript', '.js': 'text/javascript', '.json': 'application/json' }
const server = createServer((req, res) => {
  const url = new URL(req.url, 'http://x')
  if (url.pathname === '/') {
    res.writeHead(200, { 'Content-Type': 'text/html' })
    res.end(html)
    return
  }
  if (url.pathname.startsWith('/mermaid/')) {
    const file = path.join(mermaidDist, path.normalize(url.pathname.slice('/mermaid/'.length)))
    if (file.startsWith(mermaidDist) && existsSync(file)) {
      res.writeHead(200, { 'Content-Type': types[path.extname(file)] ?? 'application/octet-stream' })
      res.end(readFileSync(file))
      return
    }
  }
  res.writeHead(404)
  res.end()
})

const chrome = [
  process.env.CHROME_PATH,
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].find((candidate) => candidate && existsSync(candidate))
if (!chrome) {
  console.error('No Chrome found. Point CHROME_PATH at one.')
  process.exit(2)
}

server.listen(0, '127.0.0.1', () => {
  const { port } = server.address()
  // Asynchronously: the server above has to keep answering while Chrome loads
  // the page, and a synchronous spawn would stop the loop it runs on.
  const child = spawn(chrome, [
    '--headless=new',
    '--disable-gpu',
    '--no-first-run',
    '--virtual-time-budget=30000',
    '--dump-dom',
    `http://127.0.0.1:${port}/`,
  ])
  let dom = ''
  child.stdout.on('data', (chunk) => (dom += chunk))
  child.on('close', () => {
    server.close()
    const found = /<pre id="result">DONE(.*?)<\/pre>/s.exec(dom)
    if (!found) {
      console.error(`Chrome did not finish rendering. It printed: ${dom.slice(0, 600)}`)
      process.exit(2)
    }
    const unescape = (text) => text.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&amp;/g, '&')
    const rows = JSON.parse(unescape(found[1]))
    let bad = 0
    for (const row of rows) {
      const drew = row.kind === 'sequenceDiagram' ? row.actors > 0 && row.messages > 0 : row.nodes > 0
      const ok = !row.error && drew
      if (!ok) bad++
      const counts = row.kind === 'sequenceDiagram' ? `${row.actors} participants, ${row.messages} messages` : `${row.nodes} nodes`
      console.log(`${ok ? 'ok  ' : 'FAIL'} ${row.file}:${row.line}  ${row.kind}  ${row.error ? (row.message ?? 'drew the error diagram') : counts}`)
    }
    console.log(`${rows.length} diagram${rows.length === 1 ? '' : 's'}, ${bad} not drawn`)
    process.exit(bad === 0 ? 0 : 1)
  })
})
