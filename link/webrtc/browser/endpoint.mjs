import { Session, RECORD, ENDPOINT_ID } from 'rutis-session-v5'
import { decode } from 'rutis-codec-v5'
import { bindInvocation } from './scope.mjs'

const encoder = new TextEncoder()
const decoder = new TextDecoder('utf-8', { fatal: true })

// Visit RPC lists and records without cloning ordinary values or inspecting
// remote objects, typed buffers, futures, or other tagged protocol values.
function mapFunctions(value, map, seen = new Set(), preserveObject = () => false) {
  if (typeof value === 'function') return map(value)
  if (!value || typeof value !== 'object' || seen.has(value)) return value
  if (
    !Array.isArray(value) &&
    Object.getPrototypeOf(value) !== Object.prototype &&
    Object.getPrototypeOf(value) !== null
  )
    return value
  if (preserveObject(value)) return value
  seen.add(value)
  let descriptors
  for (const key of Object.keys(value)) {
    const field = Object.getOwnPropertyDescriptor(value, key)
    if (!field || !('value' in field)) continue
    const next = mapFunctions(field.value, map, seen, preserveObject)
    if (next === field.value) continue
    descriptors ??= Object.getOwnPropertyDescriptors(value)
    descriptors[key] = { ...field, value: next }
  }
  seen.delete(value)
  if (!descriptors) return value
  const copy = Array.isArray(value) ? [] : Object.create(Object.getPrototypeOf(value))
  return Object.defineProperties(copy, descriptors)
}

// One page endpoint owns explicitly exported services and a single session.
// Keep the peer protocol and wire value/reference handling in rutis itself.
export function createPageEndpoint(
  id,
  {
    onServiceError = ({ service, operation }) => console.warn(`rutis: ${operation} rejected for ${service}`),
  } = {},
) {
  if (typeof id !== 'string' || !ENDPOINT_ID.test(id)) throw Error('invalid endpoint id')
  if (typeof onServiceError !== 'function') throw Error('invalid service error handler')
  const local = new Map()
  // A returned reference must travel home as the exact function rutis gave
  // us. Local callbacks likewise keep one exported identity across calls.
  const remoteFunctions = new WeakMap(),
    exports = new WeakMap(),
    homes = new WeakMap()
  const localObjects = new WeakSet()
  function keepObject(value) {
    if (Array.isArray(value) || value[RECORD] === true) return false
    const methods = Object.keys(value).some(
      (key) => typeof Object.getOwnPropertyDescriptor(value, key)?.value === 'function',
    )
    if (methods) localObjects.add(value)
    return methods
  }
  function toWire(value) {
    return mapFunctions(
      value,
      (fn) => {
        if (remoteFunctions.has(fn)) return remoteFunctions.get(fn)
        if (!exports.has(fn)) {
          const exported = (...args) => handle(fn, undefined, args)
          exports.set(fn, exported)
          homes.set(exported, fn)
        }
        return exports.get(fn)
      },
      new Set(),
      keepObject,
    )
  }
  function fromWire(value) {
    const scoped = new Map()
    return mapFunctions(
      value,
      (fn) => {
        if (homes.has(fn)) return homes.get(fn)
        if (!scoped.has(fn)) {
          const callback = bindInvocation((...args) => invoke(fn, args))
          scoped.set(fn, callback)
          remoteFunctions.set(callback, fn)
        }
        return scoped.get(fn)
      },
      new Set(),
      (obj) => localObjects.has(obj),
    )
  }
  function handle(fn, receiver, args) {
    const result = fn.apply(receiver, fromWire(args))
    return result instanceof Promise ? result.then(toWire) : toWire(result)
  }
  function invoke(fn, args) {
    const read = bindInvocation(fromWire)
    return Promise.resolve(fn(...toWire(args))).then(read)
  }
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
    const connection = { remote, announce, withdraw }
    const session = new Session({
      endpoint: { local: id },
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
          if (closed) return
          if (method === 'link.offers') {
            end(error)
            far.close({ kind: 'done' })
          } else {
            onServiceError({ service: value.name, operation: method, error })
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
        const methods = new Map()
        for (const key of Object.keys(value.shape)) {
          if (key in Object.prototype || typeof value.service?.[key] !== 'function')
            throw Error('invalid service')
          const fn = value.service[key]
          // Records carry function references. On an object reference the
          // runtime's proxy sends a method call to the owning object instead.
          methods.set(
            key,
            Object.hasOwn(value.service, key)
              ? (...args) => session.callFunctionAsync(fn, args)
              : (...args) => fn(...args),
          )
        }
        remote.set(name, methods)
        if (local.has(name)) {
          // Retain the peer's current version so withdrawing the local
          // provider resolves the collision without a new announcement.
          versions.set(name, version)
          throw Error('service already provided')
        }
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
  async function call(connection, name, method, ...args) {
    const methodCall = !local.has(name) && connection?.remote.get(name)?.get(method)
    if (!methodCall) throw Error('remote service method unavailable')
    return invoke(methodCall, args)
  }
  return {
    sessions: { open },
    call: (...args) => call(current, ...args),
    captureCall: () => bindInvocation(call.bind(undefined, current)),
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
          return handle(value, handlers, args)
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
    remoteServices: () => [...(current?.remote.keys() ?? [])].filter((name) => !local.has(name)),
  }
}
