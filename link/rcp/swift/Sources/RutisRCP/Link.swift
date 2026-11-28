import Foundation

/// One ordered binary WebSocket. abort must unblock outstanding reads and writes.
public protocol RCPBinarySocket: Sendable {
    func setReadLimit(_ bytes: Int)
    func read() async throws -> Data
    func write(_ bytes: Data) async throws
    func abort()
}

public struct RCPReceiver: Sendable {
    let receive: @Sendable (Data) async throws -> Void
    let ended: @Sendable (RCPError) async -> Void
    public init(receive: @escaping @Sendable (Data) async throws -> Void,
                ended: @escaping @Sendable (RCPError) async -> Void = { _ in }) {
        self.receive = receive
        self.ended = ended
    }
}

private func sleepMillis(_ ms: Int) async throws {
    try await Task.sleep(nanoseconds: UInt64(max(0, ms)) * 1_000_000)
}

private func bounded<T: Sendable>(_ ms: Int, _ failure: RCPError, _ socket: any RCPBinarySocket,
                                  _ work: @escaping @Sendable () async throws -> T) async throws -> T {
    try await withThrowingTaskGroup(of: T.self) { group in
        group.addTask { try await work() }
        group.addTask { try await sleepMillis(ms); throw failure }
        defer { group.cancelAll() }
        do { return try await group.next()! }
        catch { socket.abort(); throw error }
    }
}

public enum RCPCloseReason: UInt8, Sendable { case done = 4, replaced, rejected }

public actor RCPSender {
    public nonisolated let limits: RCPLimits
    private let tx: Direction
    private let socket: any RCPBinarySocket
    private var busy = false
    private var waiting: [CheckedContinuation<Void, Never>] = []
    fileprivate var localClose = false
    fileprivate var ended = false
    fileprivate var writeFailure: RCPError?
    init(_ tx: Direction, _ limits: RCPLimits, _ socket: any RCPBinarySocket) {
        self.tx = tx; self.limits = limits; self.socket = socket
    }

    fileprivate func write(_ kind: UInt8, _ body: Data = Data()) async throws {
        if busy { await withCheckedContinuation { waiting.append($0) } }
        busy = true
        defer {
            if waiting.isEmpty { busy = false } else { waiting.removeFirst().resume() }
        }
        guard !ended, !localClose || kind >= 4 else { throw RCPError.done }
        if let writeFailure { throw writeFailure }
        do {
            let frame = try tx.seal(kind, body)
            try await bounded(kind >= 4 ? 1000 : 3 * limits.heartbeatMillis, .transport, socket) { [socket] in
                try await socket.write(frame)
            }
        } catch {
            writeFailure = error as? RCPError ?? .transport
            socket.abort()
            throw error
        }
    }
    public func send(_ message: Data) async throws { try await write(1, message) }
    public func close(_ reason: RCPCloseReason = .done) async {
        guard !localClose, !ended else { return }
        localClose = true
        // Never queue a final notification behind a stalled application write.
        if !busy { try? await write(reason.rawValue) }
        socket.abort()
    }
    fileprivate func finish() { ended = true; socket.abort() }
}

private func pair(_ socket: any RCPBinarySocket, _ grant: RCPGrant, _ limits: RCPLimits)
    async throws -> (RCPLimits, Direction, Direction) {
    let state = try Pairing(grant)
    let first = grant.role == .client ? try state.write(limits.offer) : nil
    @Sendable func receiveOffer() async throws -> Data {
        while true {
            let b = try await socket.read()
            try checkHandshake(b)
            if isPair(b) { return b }
            guard b.count <= 128 else { throw RCPError.size }
            guard envelope(b) else { throw RCPError.frame }
        }
    }
    let offer: Data
    if let first {
        try await socket.write(first)
        // Only this task can retransmit. Join it before advancing the Noise state.
        let repeats = Task {
            do {
                while true { try await sleepMillis(1000); try await socket.write(first) }
            } catch {
                if !Task.isCancelled { socket.abort() }
            }
        }
        do { offer = try await receiveOffer() }
        catch {
            repeats.cancel()
            await repeats.value
            throw error
        }
        repeats.cancel()
        await repeats.value
    } else { offer = try await receiveOffer() }
    let agreed = try limits.negotiate(state.read(offer))
    if first == nil { try await socket.write(state.write(limits.offer)) }
    let (tx, rx) = try state.split(limit: agreed.maxMessage)
    if first != nil { try await socket.write(tx.seal(0)) }
    while true {
        let b = try await socket.read()
        guard b.count <= 128 else { throw RCPError.size }
        try checkHandshake(b)
        if first == nil && b == offer { continue }
        guard try rx.open(b).0 == 0 else { throw RCPError.frame }
        break
    }
    if first == nil { try await socket.write(tx.seal(0)) }
    try grant.validate()
    socket.setReadLimit(agreed.maxMessage + 33)
    return (agreed, tx, rx)
}

public enum RCPLink {
    public typealias Open = @Sendable (RCPSender) async throws -> RCPReceiver

    /// Serve either role on an already-upgraded socket, including a direct listener's accepted socket.
    public static func serve(_ socket: any RCPBinarySocket, grant: RCPGrant, limits: RCPLimits = .standard,
                             open: @escaping Open) async throws {
        try await withTaskCancellationHandler {
            defer { socket.abort() }
            try grant.validate()
            socket.setReadLimit(128)
            let ms = Int(min(10, max(0, grant.expiresAt.timeIntervalSinceNow)) * 1000)
            let agreed: RCPLimits, tx: Direction, rx: Direction
            do {
                (agreed, tx, rx) = try await handshakeBefore(ms, socket, grant, limits)
            } catch {
                try Task.checkCancellation()
                let reason = grant.expiresAt <= Date() ? RCPError.expired : (error as? RCPError ?? .transport)
                let rejection = reason == .version ? rejectVersion :
                    ([.expired, .credential].contains(reason) ? rejectCredential : nil)
                if let rejection {
                    try? await bounded(1000, .transport, socket) { try await socket.write(rejection) }
                }
                throw reason
            }
            let sender = RCPSender(tx, agreed, socket)
            let receiver: RCPReceiver
            do { receiver = try await open(sender) }
            catch {
                await sender.finish()
                try Task.checkCancellation()
                if await sender.localClose { return }
                throw await sender.writeFailure ?? RCPError.refused
            }
            var outcome: RCPError = .done
            do {
                try await withThrowingTaskGroup(of: Void.self) { group in
                    group.addTask {
                        while true { try await sleepMillis(agreed.heartbeatMillis); try await sender.write(2) }
                    }
                    group.addTask {
                        while !(await sender.localClose) {
                            let bytes = try await bounded(3 * agreed.heartbeatMillis, .transport, socket) { try await socket.read() }
                            let (kind, data) = try rx.open(bytes)
                            switch kind {
                            case 1:
                                do { try await receiver.receive(data) }
                                catch {
                                    try Task.checkCancellation()
                                    throw await sender.writeFailure ?? RCPError.refused
                                }
                            case 2: try await sender.write(3)
                            case 3: continue
                            case 4: throw RCPError.peerClosed
                            case 5: throw RCPError.replaced
                            case 6: throw RCPError.refused
                            default: throw RCPError.frame
                            }
                        }
                    }
                    defer { group.cancelAll(); socket.abort() }
                    _ = try await group.next()
                }
            } catch {
                if !(await sender.localClose) && !Task.isCancelled {
                    let received = error as? RCPError ?? .transport
                    // Cancelling a concurrent heartbeat must not mask a received close record.
                    outcome = received == .transport ? (await sender.writeFailure ?? received) : received
                }
            }
            await sender.finish()
            await receiver.ended(outcome)
            try Task.checkCancellation()
            if outcome != .done { throw outcome }
        } onCancel: { socket.abort() }
    }

    /// The task owns one device. Cancelling it unloads the link and stops retries.
    public static func run(limits: RCPLimits = .standard, provider: @Sendable () async throws -> RCPGrant,
                           onRetry: @Sendable (RCPError, Int) -> Void = { _, _ in }, open: @escaping Open) async throws {
        var delay = 250, refreshed = false
        while true {
            try Task.checkCancellation()
            let admitted = Admission()
            do {
                let grant: RCPGrant
                do { grant = try await provider() }
                catch let error as RCPError { throw error }
                catch let error as URLError where error.code == .timedOut { throw RCPError.provider }
                catch { try Task.checkCancellation(); throw RCPError.credential }
                try grant.validate()
                let socket = try AppleSocket(grant.address)
                defer { socket.abort() }
                try await withTaskCancellationHandler {
                    try await bounded(10_000, .transport, socket) { try await socket.upgrade() }
                } onCancel: { socket.abort() }
                try await serve(socket, grant: grant, limits: limits) { sender in
                    await admitted.mark()
                    return try await open(sender)
                }
                return
            } catch {
                try Task.checkCancellation()
                let reason = error as? RCPError ?? .transport
                if await admitted.value { delay = 250; refreshed = false }
                switch reason {
                case .credential, .expired:
                    if refreshed { throw reason }; refreshed = true
                case .transport, .offline, .unavailable, .provider, .peerClosed, .restarted, .size: break
                default: throw reason
                }
                onRetry(reason, delay)
                try await sleepMillis(delay)
                delay = min(2 * delay, 30_000)
            }
        }
    }
}

private actor Admission {
    var value = false
    func mark() { value = true }
}

// Abort only on deadline. A protocol rejection still needs the live socket for its five-byte notification.
private func handshakeBefore(_ ms: Int, _ socket: any RCPBinarySocket, _ grant: RCPGrant, _ limits: RCPLimits)
    async throws -> (RCPLimits, Direction, Direction) {
    try await withThrowingTaskGroup(of: (RCPLimits, Direction, Direction).self) { group in
        group.addTask { try await pair(socket, grant, limits) }
        group.addTask { try await sleepMillis(ms); throw RCPError.offline }
        defer { group.cancelAll() }
        do { return try await group.next()! }
        catch RCPError.offline { socket.abort(); throw RCPError.offline }
    }
}
