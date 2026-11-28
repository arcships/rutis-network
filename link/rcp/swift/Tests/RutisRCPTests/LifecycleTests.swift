import XCTest
@testable import RutisRCP

final class LifecycleTests: XCTestCase {
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
