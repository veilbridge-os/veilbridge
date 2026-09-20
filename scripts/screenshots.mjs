#!/usr/bin/env node
// Capture the README screenshots from a demo instance.
//
// The daemon is started with -demo, so every value on screen comes from the
// in-memory adapter and the IANA documentation ranges (RFC 5737). No stand, no
// router and no real endpoint is involved, which is what makes the output safe
// to publish.
//
// Usage:
//   go build -tags ui -o /tmp/veilbridged ./cmd/veilbridged
//   /tmp/veilbridged -config /tmp/demo.json -set-password demo-password
//   /tmp/veilbridged -demo -config /tmp/demo.json -listen 127.0.0.1:8099 &
//   node scripts/screenshots.mjs
//
// Env: VB_URL (default http://127.0.0.1:8099), VB_PASSWORD (default
// demo-password), VB_OUT (default docs/img), VB_WIDTH (default 1280).

import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdir, writeFile, rm } from 'node:fs/promises'
import { setTimeout as sleep } from 'node:timers/promises'

const URL_BASE = process.env.VB_URL ?? 'http://127.0.0.1:8099'
const PASSWORD = process.env.VB_PASSWORD ?? 'demo-password'
const OUT_DIR = process.env.VB_OUT ?? 'docs/img'
const WIDTH = Number(process.env.VB_WIDTH ?? 1280)
const CHROME =
  process.env.CHROME_PATH ??
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const PROFILE = '/tmp/vb-screenshots-profile'
const PORT = 9333

// Screens to capture: route hash, output file, and the text that proves the
// screen finished loading (so we never photograph a spinner).
const SHOTS = [
  // The ready marker is a string the finished screen contains and a spinner
  // does not, so a slow load can never be photographed as an empty panel.
  { hash: '#/', file: 'dashboard.png', ready: 'Coming later', height: 840 },
  { hash: '#/nodes', file: 'nodes.png', ready: 'Endpoint', height: 420 },
]

class CDP {
  constructor(ws) {
    this.ws = ws
    this.id = 0
    this.pending = new Map()
    ws.addEventListener('message', (ev) => {
      let msg
      try {
        msg = JSON.parse(ev.data)
      } catch (err) {
        // A malformed frame must not take the whole capture down: CDP events we
        // do not await are frequent, and only replies we are waiting for matter.
        console.error(`ignoring malformed CDP frame: ${err.message}`)
        return
      }
      const p = this.pending.get(msg.id)
      if (!p) return
      this.pending.delete(msg.id)
      if (msg.error) {
        p.reject(new Error(JSON.stringify(msg.error)))
      } else {
        p.resolve(msg.result)
      }
    })
  }

  send(method, params = {}) {
    const id = ++this.id
    this.ws.send(JSON.stringify({ id, method, params }))
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }))
  }

  async evaluate(expression) {
    const r = await this.send('Runtime.evaluate', {
      expression,
      awaitPromise: true,
      returnByValue: true,
    })
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.text)
    return r.result.value
  }
}

async function waitForText(cdp, text, timeoutMs = 10000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const found = await cdp.evaluate(`document.body.innerText.includes(${JSON.stringify(text)})`)
    if (found) return
    await sleep(200)
  }
  throw new Error(`timed out waiting for ${JSON.stringify(text)}`)
}

async function main() {
  await rm(PROFILE, { recursive: true, force: true })
  await mkdir(OUT_DIR, { recursive: true })

  const chrome = spawn(CHROME, [
    '--headless=new',
    `--remote-debugging-port=${PORT}`,
    `--user-data-dir=${PROFILE}`,
    '--no-first-run',
    '--hide-scrollbars',
    'about:blank',
  ])
  chrome.on('error', (e) => {
    console.error(`cannot start Chrome (set CHROME_PATH): ${e.message}`)
    process.exit(1)
  })

  try {
    // Chrome needs a moment before the debugging endpoint answers.
    let target
    for (let i = 0; i < 50 && !target; i++) {
      await sleep(200)
      try {
        const resp = await fetch(`http://127.0.0.1:${PORT}/json/new?about:blank`, { method: 'PUT' })
        if (resp.ok) target = await resp.json()
      } catch {
        /* not up yet */
      }
    }
    if (!target) throw new Error('Chrome DevTools endpoint did not come up')

    const ws = new WebSocket(target.webSocketDebuggerUrl)
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true })
      ws.addEventListener('error', reject, { once: true })
    })
    const cdp = new CDP(ws)
    await cdp.send('Page.enable')
    await cdp.send('Runtime.enable')

    // Log in once via the API and persist the token the way the UI does, so the
    // capture starts on the screen we actually want.
    await cdp.send('Page.navigate', { url: URL_BASE })
    await sleep(800)
    await cdp.evaluate(`(async () => {
      const r = await fetch('/api/v1/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: ${JSON.stringify(PASSWORD)} }),
      })
      if (!r.ok) throw new Error('login failed: ' + r.status)
      const { token } = await r.json()
      localStorage.setItem('veilbridge.token', token)
      localStorage.setItem('veilbridge.locale', 'en')
    })()`)

    for (const shot of SHOTS) {
      await cdp.send('Emulation.setDeviceMetricsOverride', {
        width: WIDTH,
        height: shot.height,
        deviceScaleFactor: 2,
        mobile: false,
      })
      await cdp.send('Page.navigate', { url: `${URL_BASE}/${shot.hash}` })
      await sleep(600)
      // The SPA keeps the locale in memory; force English for published images.
      await cdp.evaluate(`localStorage.setItem('veilbridge.locale', 'en')`)
      await cdp.send('Page.reload')
      await sleep(600)
      await waitForText(cdp, shot.ready)
      const { data } = await cdp.send('Page.captureScreenshot', { format: 'png' })
      const path = `${OUT_DIR}/${shot.file}`
      await writeFile(path, Buffer.from(data, 'base64'))
      console.log(`wrote ${path}`)
    }
    ws.close()
  } finally {
    chrome.kill()
    // Chrome keeps writing to its profile while it is dying, so a remove
    // issued in the same millisecond races it and fails with ENOTEMPTY — which
    // is what happened here, after the images had already been written.
    // Wait for the exit, retry, and never let a temp-directory cleanup fail a
    // capture that already succeeded.
    await once(chrome, 'exit').catch(() => {})
    await rm(PROFILE, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 }).catch(
      (err) => console.error(`could not remove ${PROFILE}: ${err.message}`),
    )
  }
}

await main()
