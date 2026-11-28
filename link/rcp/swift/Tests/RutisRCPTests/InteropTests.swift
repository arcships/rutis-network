import XCTest
@testable import RutisRCP

final class InteropTests: XCTestCase {
    func testWebSocketPeer() async throws {
        guard let file = ProcessInfo.processInfo.environment["RCP_FIXTURE"] else {
            throw XCTSkip("Run the Go mobile interop matrix to supply a peer")
        }
        let values = try JSONDecoder().decode([String: String].self, from: Data(contentsOf: URL(fileURLWithPath: file)))
        let mine = try XCTUnwrap(values["role"])
        let other = mine == "client" ? "daemon" : "client"
        let grant = RCPGrant(address: try XCTUnwrap(URL(string: try XCTUnwrap(values["address"]))), role: mine == "client" ? .client : .daemon,
                             privateKey: try XCTUnwrap(Data(base64Encoded: values["privateKey"]!)),
                             peerKey: try XCTUnwrap(Data(base64Encoded: values["peerKey"]!)),
                             binding: try XCTUnwrap(Data(base64Encoded: values["binding"]!)), expiresAt: Date().addingTimeInterval(60))
        if values["mode"] == "fault" {
            let fault = values["fault"]!
            let reason = try XCTUnwrap(RCPError(rawValue: values["reason"]!))
            let terminal = ["version","invalid","revoked","slow"].contains(fault)
            let counts = Counts()
            var result: RCPError?
            do {
                try await RCPLink.run(limits: RCPLimits(maxMessage: 4096, heartbeatMillis: 150), provider: {
                    let attempt = counts.attempt()
                    if attempt > 2 { throw RCPError.refused }
                    if fault == "expired" && attempt == 1 { throw RCPError.expired }
                    if fault == "invalid" {
                        return RCPGrant(address: grant.address, role: .client, privateKey: grant.privateKey,
                                        peerKey: grant.peerKey, binding: Data([0]), expiresAt: grant.expiresAt)
                    }
                    return grant
                }, onRetry: { reason, delay in
                    XCTAssertEqual(delay, 250)
                    counts.retry(reason)
                }) { sender in
                    let attempt = counts.opened()
                    if fault != "slow" && !(fault == "silent" && attempt == 1) { await sender.close() }
                    return RCPReceiver(receive: { message in
                        XCTAssertEqual(fault, "slow")
                        try await Task.sleep(nanoseconds: 60_000_000)
                        let index = counts.nextMessage()
                        XCTAssertEqual(message, Data(repeating: UInt8(index), count: 4096))
                    }, ended: { counts.end($0) })
                }
            } catch let why as RCPError { result = why }
            XCTAssertEqual(result, terminal ? reason : nil)
            XCTAssertEqual(counts.retries, ["version","slow"].contains(fault) ? [] : [reason])
            XCTAssertEqual(counts.calls, ["version","slow"].contains(fault) ? 1 : 2)
            let endings: [RCPError]
            switch fault {
            case "silent": endings = [.transport, .done]
            case "slow": endings = [.replaced]
            default: endings = terminal ? [] : [.done]
            }
            XCTAssertEqual(counts.endings, endings)
            XCTAssertEqual(counts.opens, endings.count)
            if fault == "slow" { XCTAssertEqual(counts.messages, 3) }
            return
        }
        actor Exchange {
            var greeted = false, acknowledged = false
            func accept(_ bytes: Data, _ sender: RCPSender, _ mine: String, _ other: String) async throws {
                switch String(decoding: bytes, as: UTF8.self) {
                case "visible-only-at-the-rutis-endpoints-" + other:
                    greeted = true
                    try await sender.send(Data(("ack-" + other).utf8))
                case "ack-" + mine:
                    XCTAssertTrue(greeted)
                    acknowledged = true
                    await sender.close()
                default: XCTFail("unexpected message")
                }
            }
        }
        let exchange = Exchange()
        try await RCPLink.run(limits: RCPLimits(maxMessage: 4096, heartbeatMillis: 150), provider: { grant }) { sender in
            try await sender.send(Data(("visible-only-at-the-rutis-endpoints-" + mine).utf8))
            return RCPReceiver(receive: { try await exchange.accept($0, sender, mine, other) })
        }
        let acknowledged = await exchange.acknowledged
        XCTAssertTrue(acknowledged)
    }
}

private final class Counts: @unchecked Sendable {
    private let lock = NSLock()
    private(set) var calls = 0, opens = 0
    private(set) var retries = [RCPError]()
    func attempt() -> Int { lock.lock(); defer { lock.unlock() }; calls += 1; return calls }
    func retry(_ why: RCPError) { lock.lock(); defer { lock.unlock() }; retries.append(why) }
    private(set) var endings = [RCPError](), messages = 0
    func opened() -> Int { lock.lock(); defer { lock.unlock() }; opens += 1; return opens }
    func nextMessage() -> Int { lock.lock(); defer { lock.unlock() }; messages += 1; return messages }
    func end(_ reason: RCPError) { lock.lock(); defer { lock.unlock() }; endings.append(reason) }
}
