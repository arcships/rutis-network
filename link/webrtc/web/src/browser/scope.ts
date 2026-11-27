// Rutis enters this scope when dispatching each call. Where native async
// context is unavailable, retain a specific invocation at the RPC boundary.
interface Scope {
  storage: AsyncLocalStorage
  value: unknown
  parent?: Scope
}
interface Variable {
  get(): unknown
  run<T>(value: unknown, action: () => T): T
}
const context = (globalThis as typeof globalThis & { AsyncContext?: { Variable?: new () => Variable } })
  .AsyncContext
let active: Scope | undefined

export function bindInvocation<A extends unknown[], R>(fn: (...args: A) => R) {
  const scope = active
  if (!scope) return fn
  return (...args: A) => scope.storage.run(scope.value, () => fn(...args))
}

export class AsyncLocalStorage {
  #variable = context?.Variable && new context.Variable()

  getStore() {
    if (this.#variable) return this.#variable.get()
    for (let scope = active; scope; scope = scope.parent) {
      if (scope.storage === this) return scope.value
    }
  }

  run<T>(value: unknown, action: () => T): T {
    const parent = active
    active = { storage: this, value, parent }
    try {
      return this.#variable ? this.#variable.run(value, action) : action()
    } finally {
      active = parent
    }
  }
}
