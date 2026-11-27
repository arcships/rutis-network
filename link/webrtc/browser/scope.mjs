// Page services make asynchronous remote calls. Preserve the synchronous
// part of the dispatch chain on browsers without the AsyncContext proposal.
export class AsyncLocalStorage {
  #value
  #variable = globalThis.AsyncContext?.Variable && new globalThis.AsyncContext.Variable()
  getStore() {
    return this.#variable ? this.#variable.get() : this.#value
  }
  run(value, action) {
    if (this.#variable) return this.#variable.run(value, action)
    const previous = this.#value
    this.#value = value
    try {
      return action()
    } finally {
      this.#value = previous
    }
  }
}
