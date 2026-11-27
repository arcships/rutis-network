import { createServer } from 'node:http'
import { createHmac, timingSafeEqual } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { createPairing } from '../web/src/node/pairing.ts'

const secrets = [process.env.PAIR_A_TOKEN, process.env.PAIR_B_TOKEN]
if (secrets.some((x) => !x || x.length < 20) || secrets[0] === secrets[1])
  throw Error('Set distinct PAIR_A_TOKEN and PAIR_B_TOKEN (at least 20 characters each)')
const keys = secrets.map((x) => Buffer.from('Bearer ' + x))
const pairId = process.env.PAIR_ID ?? 'desk-and-page'
const turnUrl = process.env.TURN_URL
const turnSecret = process.env.TURN_SECRET
if (Boolean(turnUrl) !== Boolean(turnSecret)) throw Error('Set TURN_URL and TURN_SECRET together')
const turnLifetime = Number(process.env.TURN_TTL_SECONDS ?? 3600)
if (!Number.isSafeInteger(turnLifetime) || turnLifetime < 2) throw Error('invalid TURN_TTL_SECONDS')
const signaling = createPairing({
  lifetimeMs: Math.min(60000, turnLifetime * 1000),
  refreshAfterMs: turnUrl ? turnLifetime * 500 : undefined,
  pairId,
  members: ['a', 'b'],
  iceServers(member, _expiresAt, issuedAt) {
    const servers = []
    if (process.env.STUN_URL) servers.push({ urls: [process.env.STUN_URL] })
    if (turnUrl) {
      const username = Math.floor(issuedAt / 1000) + turnLifetime + ':' + member
      const credential = createHmac('sha1', turnSecret).update(username).digest('base64')
      servers.push({ urls: [turnUrl], username, credential })
    }
    return servers
  },
})
const htmlFile = new URL('./index.html', import.meta.url)
const dev = process.argv.includes('--dev')
let devMiddleware
let bundle = dev ? undefined : await readFile(new URL('../dist/link-webrtc.mjs', import.meta.url))
const server = createServer(async (req, res) => {
  try {
    res.setHeader('cache-control', 'no-store')
    if (req.method === 'GET' && req.url === '/') {
      res.setHeader('content-type', 'text/html; charset=utf-8')
      const html = await readFile(htmlFile, 'utf8')
      const reload = dev ? '<script type="module" src="/@vite/client"></script>' : ''
      return res.end(html.replace('</html>', reload + '</html>'))
    }
    if (req.method === 'GET' && req.url === '/link-webrtc.mjs') {
      res.setHeader('content-type', 'text/javascript')
      return res.end(bundle)
    }
    const method = /^\/signal\/(acquire|publish|receive)$/.exec(req.url)?.[1]
    if (req.method !== 'POST' || !method) {
      if (devMiddleware && req.method === 'GET' && /^\/@(?:vite|fs)\//.test(req.url))
        return devMiddleware(req, res)
      res.writeHead(404)
      return res.end()
    }
    const supplied = Buffer.from(req.headers.authorization ?? '')
    const index = keys.findIndex((key) => key.length === supplied.length && timingSafeEqual(key, supplied))
    if (index < 0) {
      res.writeHead(403)
      return res.end()
    }
    let size = 0
    const chunks = []
    for await (const chunk of req) {
      size += chunk.length
      if (size > 512 * 1024) {
        res.writeHead(413)
        return res.end()
      }
      chunks.push(chunk)
    }
    const value = await signaling(
      index === 0 ? 'a' : 'b',
      method,
      JSON.parse(Buffer.concat(chunks).toString('utf8')),
    )
    res.setHeader('content-type', 'application/json')
    res.end(JSON.stringify(value))
  } catch (e) {
    res.writeHead(e.name === 'PairingDenied' ? 403 : 409)
    res.end()
  }
})
if (dev) {
  const { startDevPage } = await import('../web/scripts/dev.mjs')
  devMiddleware = await startDevPage(server, (bytes) => {
    bundle = bytes
  })
}
server.listen(Number(process.env.PORT ?? 8080), '127.0.0.1', () =>
  console.log('WebRTC example ready at http://127.0.0.1:' + server.address().port),
)
