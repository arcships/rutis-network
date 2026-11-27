import { Session, RECORD, ENDPOINT_ID } from 'rutis-session-v5'
import { decode } from 'rutis-codec-v5'

const encoder = new TextEncoder()
const decoder = new TextDecoder('utf-8', { fatal: true })

// One page endpoint owns explicitly exported services and a single session.
// Keep the peer protocol and wire value/reference handling in rutis itself.
export function createPageEndpoint(id) {
  if (typeof id !== 'string' || !ENDPOINT_ID.test(id)) throw Error('invalid endpoint id')
  const local = new Map()
  let current,
    version = 0
  function open(far) {
    if (current) throw Error('page endpoint is already connected')
    const remote = new Map(),
      versions = new Map()
    let closed = false,
      writes = Promise.resolve(),
      queuedBytes = 0,
      queuedFrames = 0
    let offerVersion = -1,
      serviceEpoch
    const connection = { remote, announce, withdraw, call: (fn, args) => session.callFunctionAsync(fn, args) }
    const session = new Session({
      endpoint: { local: id, declare: ['node'] },
      dispatch,
      maxTransfer: 16 << 20,
      pump() {
        throw Error('page endpoints use asynchronous service calls')
      },
      abort(error) {
        end(error)
        far.close({ kind: 'done' })
      },
      send(text) {
        if (closed) throw Error('session closed')
        const bytes = encoder.encode(text)
        if (queuedBytes + bytes.length > 32 << 20 || queuedFrames >= 1024) {
          const error = Error('page send queue full')
          end(error)
          far.close({ kind: 'done' })
          throw error
        }
        queuedBytes += bytes.length
        queuedFrames++
        writes = writes
          .then(async () => {
            if (!closed) await far.send(bytes)
          })
          .catch((error) => {
            if (!closed) {
              end(error)
              far.close({ kind: 'done' })
            }
          })
          .finally(() => {
            queuedBytes -= bytes.length
            queuedFrames--
          })
      },
    })
    function end(error) {
      if (closed) return
      closed = true
      clearTimeout(helloTimer)
      session.close(error)
      remote.clear()
      if (current === connection) current = undefined
    }
    function notify(method, value) {
      if (!closed)
        session.invokeAsync('', method, [value]).catch((error) => {
          if (!closed) {
            end(error)
            far.close({ kind: 'done' })
          }
        })
    }
    function announce(entry) {
      if (serviceEpoch !== undefined)
        notify('services.announce', {
          name: entry.name,
          plugin: entry.name,
          shape: entry.shape,
          service: entry.methods,
          version: entry.version,
        })
    }
    function withdraw(name, version) {
      if (serviceEpoch !== undefined) notify('services.withdraw', { name, version })
    }
    function dispatch(target, method, [value]) {
      if (target !== '' || !value || typeof value !== 'object') throw Error('unknown endpoint operation')
      if (method === 'link.offers') {
        if (value.version <= offerVersion) return
        offerVersion = value.version
        const epoch = value.families.includes('services') ? (value.since?.services ?? 0) : undefined
        if (epoch !== serviceEpoch) {
          serviceEpoch = epoch
          for (const entry of local.values()) announce(entry)
        }
        return
      }
      if (!['services.announce', 'services.withdraw'].includes(method))
        throw Error('unknown endpoint operation')
      const { name, version } = value
      if (typeof name !== 'string' || !Number.isSafeInteger(version)) throw Error('invalid announcement')
      if (version <= (versions.get(name) ?? -1)) return
      if (method === 'services.withdraw') remote.delete(name)
      else {
        if (local.has(name)) throw Error('service already provided')
        const methods = new Map()
        for (const key of Object.keys(value.shape)) {
          if (!Object.hasOwn(value.service, key) || typeof value.service[key] !== 'function')
            throw Error('invalid service')
          methods.set(key, value.service[key])
        }
        remote.set(name, methods)
      }
      versions.set(name, version)
    }
    const helloTimer = setTimeout(() => {
      end(Error('session hello timed out'))
      far.close({ kind: 'done' })
    }, 10000)
    current = connection
    session.ready
      .then(() => {
        clearTimeout(helloTimer)
        notify('link.offers', { families: ['services'], version: 1, since: { services: 1 } })
      })
      .catch(() => {})
    session.start()
    return {
      receive(bytes) {
        session.receive(decode(decoder.decode(bytes)))
        if (session.fault) throw session.fault
      },
      end() {
        end(Error('session closed'))
      },
    }
  }
  return {
    sessions: { open },
    provide(name, handlers) {
      if (
        typeof name !== 'string' ||
        !name ||
        name.trim() !== name ||
        /[#\r\n\0]/.test(name) ||
        name.startsWith('rutis/') ||
        local.has(name) ||
        current?.remote.has(name)
      )
        throw Error('invalid or duplicate service name')
      const methods = { [RECORD]: true },
        shape = Object.create(null)
      for (const [key, value] of Object.entries(handlers)) {
        if (typeof value !== 'function' || key in Object.prototype) throw Error('invalid service method')
        methods[key] = (...args) => {
          if (local.get(name) !== entry) throw Error('service withdrawn')
          return value.apply(handlers, args)
        }
        shape[key] = 'async'
      }
      const entry = { name, methods, shape, version: ++version }
      local.set(name, entry)
      current?.announce(entry)
      return () => {
        if (local.get(name) === entry) {
          local.delete(name)
          current?.withdraw(name, ++version)
        }
      }
    },
    remoteServices: () => [...(current?.remote.keys() ?? [])],
    async call(name, method, ...args) {
      const fn = current?.remote.get(name)?.get(method)
      if (!fn) throw Error('remote service method unavailable')
      return current.call(fn, args)
    },
  }
}
