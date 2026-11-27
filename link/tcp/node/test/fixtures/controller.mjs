import { createServer } from 'node:http'
import { definePlugin } from '@arcships/rutis'

// Test UI in the local management scope. Only the test process can reach the
// socket in its private temporary directory. No forwarding service is exported.
export default definePlugin({
  async apply(ctx, { service, socket }) {
    const control = ctx.use(service)
    const server = createServer(async (req, res) => {
      try {
        const chunks = []
        for await (const chunk of req) chunks.push(chunk)
        const [method, ...args] = JSON.parse(Buffer.concat(chunks))
        const value = await control[method](...args)
        res.end(JSON.stringify(value ?? null))
      } catch (error) {
        res.writeHead(400)
        res.end(String(error))
      }
    })
    ctx.effect(() => {
      server.closeAllConnections()
      server.close()
    })
    await new Promise((resolve, reject) => server.once('error', reject).listen(socket, resolve))
    console.log('management socket ready', service)
  },
})
