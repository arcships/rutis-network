import CryptoKit
import XCTest
@testable import RutisRCP

final class VectorTests: XCTestCase {
    func testGoWireVector() throws {
        let dir = (0..<4).reduce(URL(fileURLWithPath: #filePath)) { url, _ in url.deletingLastPathComponent() }
        let json = try JSONDecoder().decode([String: String].self, from: Data(contentsOf: dir.appendingPathComponent("testdata/wire-v1.json")))
        func v(_ name: String) -> Data {
            let hex = Array(json[name]!.utf8)
            return Data(stride(from: 0, to: hex.count, by: 2).map { UInt8(String(decoding: hex[$0..<$0+2], as: UTF8.self), radix: 16)! })
        }
        func grant(_ role: RCPRole, _ mine: String, _ peer: String) throws -> RCPGrant {
            let pin = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: v(peer+"StaticPrivate")).publicKey
            return RCPGrant(address: URL(string: "ws://127.0.0.1")!, role: role,
                            privateKey: v(mine+"StaticPrivate"), peerKey: pin.rawRepresentation,
                            binding: v("binding"), expiresAt: .distantFuture)
        }
        let client = try Pairing(grant(.client, "client", "daemon"), ephemeral: v("clientEphemeralPrivate"))
        let daemon = try Pairing(grant(.daemon, "daemon", "client"), ephemeral: v("daemonEphemeralPrivate"))
        XCTAssertEqual(try client.write(v("clientOffer")), v("clientPair"))
        XCTAssertEqual(try daemon.read(v("clientPair")), v("clientOffer"))
        XCTAssertEqual(try daemon.write(v("daemonOffer")), v("daemonPair"))
        XCTAssertEqual(try client.read(v("daemonPair")), v("daemonOffer"))
        let c = try client.split(limit: 1024), d = try daemon.split(limit: 1024)
        func record(_ name: String, _ tx: Direction, _ rx: Direction, _ kind: UInt8, _ body: Data = Data()) throws {
            let frame = try tx.seal(kind, body)
            XCTAssertEqual(frame, v(name))
            let (type, plain) = try rx.open(frame)
            XCTAssertEqual(type, kind)
            XCTAssertEqual(plain, body)
        }
        try record("clientReady", c.tx, d.rx, 0)
        try record("daemonReady", d.tx, c.rx, 0)
        XCTAssertThrowsError(try d.rx.open(v("clientDataInvalidTag"))) { XCTAssertEqual($0 as? RCPError, .frame) }
        try record("clientData", c.tx, d.rx, 1, v("plain"))
        XCTAssertThrowsError(try d.rx.open(v("clientData")))
        try record("daemonEmptyData", d.tx, c.rx, 1)
        try record("clientClose", c.tx, d.rx, 4)
        let exact = Data(repeating: 1, count: 1024)
        XCTAssertEqual(try d.rx.open(c.tx.seal(1, exact)).1, exact)
        XCTAssertThrowsError(try c.tx.seal(1, exact + Data([1]))) { XCTAssertEqual($0 as? RCPError, .size) }
        XCTAssertThrowsError(try checkHandshake(v("rejectVersion"))) { XCTAssertEqual($0 as? RCPError, .version) }
        XCTAssertThrowsError(try checkHandshake(v("rejectCredential"))) { XCTAssertEqual($0 as? RCPError, .credential) }
    }
}
