import CryptoKit
import XCTest
@testable import RutisRCP

final class LifecycleTests: XCTestCase {
    func testCloseDuringWriteSendsReasonAfterData() async throws {
        actor Socket: RCPBinarySocket {
            let firstWrite: XCTestExpectation
            var frames = [Data]()
            init(_ firstWrite: XCTestExpectation) { self.firstWrite = firstWrite }
            nonisolated func setReadLimit(_ bytes: Int) {}
            nonisolated func abort() {}
            func read() async throws -> Data { throw RCPError.transport }
            func write(_ bytes: Data) async throws {
                frames.append(bytes)
                if frames.count == 1 {
                    firstWrite.fulfill()
                    try await Task.sleep(nanoseconds: 100_000_000)
                }
            }
        }
        let started = expectation(description: "first write started")
        let socket = Socket(started)
        let key = SymmetricKey(size: .bits256)
        let sender = RCPSender(Direction(key: key, limit: 128), .standard, socket)
        let data = Task { try await sender.send(Data([7])) }
        await fulfillment(of: [started], timeout: 2)
        await sender.close(.rejected)
        try await data.value
        let frames = await socket.frames
        let rx = Direction(key: key, limit: 128)
        XCTAssertEqual(try frames.map { try rx.open($0).0 }, [1, 6])
    }

    func testCancelPendingRetry() async throws {
        actor Provider {
            var attempts = 0
            func get() throws -> RCPGrant { attempts += 1; throw RCPError.provider }
        }
        let provider = Provider()
        let waiting = expectation(description: "retry scheduled")
        let row = Task {
            try await RCPLink.run(provider: { try await provider.get() }, onRetry: { reason, delay in
                XCTAssertEqual(reason, .provider)
                XCTAssertEqual(delay, 250)
                waiting.fulfill()
            }) { _ in XCTFail("admitted without a grant"); throw RCPError.refused }
        }
        await fulfillment(of: [waiting], timeout: 2)
        row.cancel()
        do { try await row.value; XCTFail("expected cancellation") } catch is CancellationError {}
        try await Task.sleep(nanoseconds: 300_000_000)
        let attempts = await provider.attempts
        XCTAssertEqual(attempts, 1)
    }
}
