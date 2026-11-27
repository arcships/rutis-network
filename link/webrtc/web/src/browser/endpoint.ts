import type { Far } from './types.ts'
import { Session, RECORD, ENDPOINT_ID } from 'rutis-session-v5'
import { decode } from 'rutis-codec-v5'
import { bindInvocation } from './scope.ts'

type Method = (...args: unknown[]) => unknown
interface Entry {
  name: string
  methods: Record<string, Method>
  shape: Record<string, string>
  version: number
}
interface Connection {
  remote: Map<string, Map<string, Method>>
  announce(entry: Entry): void
  withdraw(name: string, version: number): void
}
interface ServiceError {
  service: string
  operation: string
  error: unknown
}
const record = (value: unknown): value is Record<PropertyKey, unknown> =>
  typeof value === 'object' && value !== null

const encoder = new TextEncoder()
const decoder = new TextDecoder('utf-8', { fatal: true })

// Visit RPC lists and records without cloning ordinary values or inspecting
// remote objects, typed buffers, futures, or other tagged protocol values.
function mapFunctions(
  value: unknown,
  map: (fn: Function) => Function,
  seen = new Set<object>(),
  preserveObject: (value: object) => boolean = () => false,
): unknown {
  if (typeof value === 'function') return map(value)
  if (!record(value) || seen.has(value)) return value
  if (
    !Array.isArray(value) &&
    Object.getPrototypeOf(value) !== Object.prototype &&
    Object.getPrototypeOf(value) !== null
  )
    return value
  if (preserveObject(value)) return value
  seen.add(value)
  let descriptors: PropertyDescriptorMap | undefined
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
  id: string,
  {
    onServiceError = ({ service, operation }) => console.warn(`rutis: ${operation} rejected for ${service}`),
  }: { onServiceError?: (event: ServiceError) => void } = {},
) {
  if (typeof id !== 'string' || !ENDPOINT_ID.test(id)) throw Error('invalid endpoint id')
  if (typeof onServiceError !== 'function') throw Error('invalid service error handler')
  const local = new Map<string, Entry>()
  // A returned reference must travel home as the exact function rutis gave
  // us. Local callbacks likewise keep one exported identity across calls.
  const remoteFunctions = new WeakMap<Function, Function>(),
    exports = new WeakMap<Function, Function>(),
    homes = new WeakMap<Function, Function>()
  const localObjects = new WeakSet<object>()
  function keepObject(value: object) {
    if (Array.isArray(value) || Reflect.get(value, RECORD) === true) return false
    const methods = Object.keys(value).some(
      (key) => typeof Object.getOwnPropertyDescriptor(value, key)?.value === 'function',
    )
    if (methods) localObjects.add(value)
    return methods
  }
  function toWire(value: unknown): unknown {
    return mapFunctions(
      value,
      (fn) => {
        if (remoteFunctions.has(fn)) return remoteFunctions.get(fn)!
        if (!exports.has(fn)) {
          const exported = (...args: unknown[]) => handle(fn, undefined, args)
          exports.set(fn, exported)
          homes.set(exported, fn)
        }
        return exports.get(fn)!
      },
      new Set(),
      keepObject,
    )
  }
  function fromWire(value: unknown): unknown {
    const scoped = new Map<Function, Function>()
    return mapFunctions(
      value,
      (fn) => {
        if (homes.has(fn)) return homes.get(fn)!
        if (!scoped.has(fn)) {
          const callback = bindInvocation((...args: unknown[]) => invoke(fn, args))
          scoped.set(fn, callback)
          remoteFunctions.set(callback, fn)
        }
        return scoped.get(fn)!
      },
      new Set(),
      (obj) => localObjects.has(obj),
    )
  }
  function handle(fn: Function, receiver: unknown, args: unknown[]): unknown {
    const result = Reflect.apply(fn, receiver, fromWire(args) as unknown[])
    return result instanceof Promise ? result.then(toWire) : toWire(result)
  }
  function invoke(fn: Function, args: unknown[]): Promise<unknown> {
    const read = bindInvocation(fromWire)
    return Promise.resolve(Reflect.apply(fn, undefined, toWire(args) as unknown[])).then(read)
  }
  let current: Connection | undefined,
    version = 0
  function open(far: Far) {
    if (current) throw Error('page endpoint is already connected')
    const remote = new Map<string, Map<string, Method>>(),
      versions = new Map<string, number>()
    let closed = false,
      writes = Promise.resolve(),
      queuedBytes = 0,
      queuedFrames = 0
    let offerVersion = -1,
      serviceEpoch: number | undefined
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
    function end(error: unknown) {
      if (closed) return
      closed = true
      clearTimeout(helloTimer)
      session.close(error)
      remote.clear()
      if (current === connection) current = undefined
    }
    function notify(method: string, value: Record<string, unknown>) {
      if (!closed)
        session.invokeAsync('', method, [value]).catch((error) => {
          if (closed) return
          if (method === 'link.offers') {
            end(error)
            far.close({ kind: 'done' })
          } else {
            onServiceError({ service: String(value.name), operation: method, error })
          }
        })
    }
    function announce(entry: Entry) {
      if (serviceEpoch !== undefined)
        notify('services.announce', {
          name: entry.name,
          plugin: entry.name,
          shape: entry.shape,
          service: entry.methods,
          version: entry.version,
        })
    }
    function withdraw(name: string, version: number) {
      if (serviceEpoch !== undefined) notify('services.withdraw', { name, version })
    }
    function dispatch(target: string, method: string, [value]: unknown[]) {
      if (target !== '' || !record(value)) throw Error('unknown endpoint operation')
      if (method === 'link.offers') {
        if (typeof value.version !== 'number' || !Array.isArray(value.families)) throw Error('invalid offers')
        if (value.version <= offerVersion) return
        offerVersion = value.version
        const since = record(value.since) ? value.since.services : undefined
        const epoch = value.families.includes('services')
          ? typeof since === 'number'
            ? since
            : 0
          : undefined
        if (epoch !== serviceEpoch) {
          serviceEpoch = epoch
          for (const entry of local.values()) announce(entry)
        }
        return
      }
      if (!['services.announce', 'services.withdraw'].includes(method))
        throw Error('unknown endpoint operation')
      const { name, version } = value
      if (typeof name !== 'string' || typeof version !== 'number' || !Number.isSafeInteger(version))
        throw Error('invalid announcement')
      if (version <= (versions.get(name) ?? -1)) return
      if (method === 'services.withdraw') remote.delete(name)
      else {
        const methods = new Map<string, Method>()
        if (!record(value.shape) || !record(value.service)) throw Error('invalid service')
        for (const key of Object.keys(value.shape)) {
          const fn = value.service[key]
          if (key in Object.prototype || typeof fn !== 'function') throw Error('invalid service')
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
      receive(bytes: Uint8Array) {
        session.receive(decode(decoder.decode(bytes)))
        if (session.fault) throw session.fault
      },
      end() {
        end(Error('session closed'))
      },
    }
  }
  async function call(connection: Connection | undefined, name: string, method: string, ...args: unknown[]) {
    const methodCall = !local.has(name) && connection?.remote.get(name)?.get(method)
    if (!methodCall) throw Error('remote service method unavailable')
    return invoke(methodCall, args)
  }
  return {
    sessions: { open },
    call: (name: string, method: string, ...args: unknown[]) => call(current, name, method, ...args),
    captureCall: () => bindInvocation(call.bind(undefined, current)),
    provide(name: string, handlers: Record<string, (...args: never[]) => unknown>) {
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
      const methods: Record<string, Method> & { [RECORD]: true } = { [RECORD]: true },
        shape: Record<string, string> = Object.create(null)
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
