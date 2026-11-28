import CryptoKit
import Foundation

public enum RCPRole: Sendable { case client, daemon }

public enum RCPError: String, Error, Sendable {
    case credential = "credential-rejected", expired = "credential-expired", version = "version-mismatch"
    case frame = "invalid-frame", size = "frame-too-large", offline = "peer-offline"
    case unavailable = "relay-unavailable", provider = "credential-provider-unavailable"
    case transport = "transport-error", restarted = "peer-restarted", peerClosed = "peer-closed"
    case done, replaced, refused
}

public struct RCPGrant: Sendable {
    public let address: URL
    public let role: RCPRole
    let privateKey, peerKey, binding: Data
    public let expiresAt: Date

    public init(address: URL, role: RCPRole, privateKey: Data, peerKey: Data, binding: Data, expiresAt: Date) {
        self.address = address
        self.role = role
        self.privateKey = privateKey
        self.peerKey = peerKey
        self.binding = binding
        self.expiresAt = expiresAt
    }

    func validate() throws {
        guard expiresAt > Date() else { throw RCPError.expired }
        guard privateKey.count == 32, peerKey.count == 32, (1...1024).contains(binding.count)
        else { throw RCPError.credential }
    }
}

public struct RCPLimits: Sendable {
    public let maxMessage, heartbeatMillis: Int
    public static let standard = RCPLimits(size: 16 << 20, beat: 10_000)
    private init(size: Int, beat: Int) { maxMessage = size; heartbeatMillis = beat }

    public init(maxMessage: Int, heartbeatMillis: Int) throws {
        guard (1...(1 << 30)).contains(maxMessage), (100...300_000).contains(heartbeatMillis)
        else { throw RCPError.frame }
        self.init(size: maxMessage, beat: heartbeatMillis)
    }

    var offer: Data { bigEndian(UInt32(maxMessage)) + bigEndian(UInt32(heartbeatMillis)) }
    func negotiate(_ data: Data) throws -> RCPLimits {
        guard data.count == 8 else { throw RCPError.frame }
        let other = try RCPLimits(maxMessage: Int(uint(data.prefix(4))), heartbeatMillis: Int(uint(data.suffix(4))))
        return RCPLimits(size: min(maxMessage, other.maxMessage), beat: max(heartbeatMillis, other.heartbeatMillis))
    }
}

private func bigEndian<T: FixedWidthInteger>(_ value: T) -> Data {
    withUnsafeBytes(of: value.bigEndian) { Data($0) }
}
private func uint(_ bytes: Data) -> UInt64 { bytes.reduce(0) { ($0 << 8) | UInt64($1) } }

let version = Data([82, 67, 80, 1])
let rejectVersion = Data([82, 67, 80, 69, 1])
let rejectCredential = Data([82, 67, 80, 69, 2])
private let recordPrefix = Data([0, 0, 0, 12, 0, 0, 0, 0])
func isPair(_ b: Data) -> Bool { b.count == 60 && b.starts(with: version) }
func envelope(_ b: Data) -> Bool { b.count >= 33 && b.starts(with: recordPrefix) }
func checkHandshake(_ b: Data) throws {
    if b == rejectCredential { throw RCPError.credential }
    if b == rejectVersion || (b.count >= 4 && b.starts(with: version.prefix(3)) && !b.starts(with: version)) {
        throw RCPError.version
    }
}

private func encrypt(_ key: SymmetricKey, _ plain: Data, nonce: Data, ad: Data) throws -> Data {
    let box = try AES.GCM.seal(plain, using: key, nonce: AES.GCM.Nonce(data: nonce), authenticating: ad)
    return box.ciphertext + box.tag
}
private func decrypt(_ key: SymmetricKey, _ bytes: Data, nonce: Data, ad: Data) throws -> Data {
    guard bytes.count >= 16 else { throw RCPError.frame }
    let box = try AES.GCM.SealedBox(nonce: AES.GCM.Nonce(data: nonce), ciphertext: bytes.dropLast(16), tag: bytes.suffix(16))
    return try AES.GCM.open(box, using: key, authenticating: ad)
}

// Noise KK has exactly two messages: e/es/ss, then e/ee/se.
final class Pairing {
    typealias DH = Curve25519.KeyAgreement
    private let role: RCPRole
    private let local, ephemeral: DH.PrivateKey
    private let remote: DH.PublicKey
    private var remoteEphemeral: DH.PublicKey?
    private var hash, chain: Data
    private var step = 0

    init(_ grant: RCPGrant, ephemeral fixtureKey: Data? = nil) throws {
        try grant.validate()
        role = grant.role
        local = try DH.PrivateKey(rawRepresentation: grant.privateKey)
        remote = try DH.PublicKey(rawRepresentation: grant.peerKey)
        ephemeral = try fixtureKey.map { try DH.PrivateKey(rawRepresentation: $0) } ?? DH.PrivateKey()
        let name = Data("Noise_KK_25519_AESGCM_SHA256".utf8)
        chain = name + Data(repeating: 0, count: 32 - name.count)
        hash = chain
        mixHash(version + grant.binding)
        let mine = local.publicKey.rawRepresentation
        mixHash(role == .client ? mine : grant.peerKey)
        mixHash(role == .client ? grant.peerKey : mine)
    }

    private func mixHash(_ bytes: Data) { hash = Data(SHA256.hash(data: hash + bytes)) }
    private func derive(_ input: Data) -> (Data, SymmetricKey) {
        let key = HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: input), salt: chain,
                                        info: Data(), outputByteCount: 64)
        return key.withUnsafeBytes { (Data($0.prefix(32)), SymmetricKey(data: $0.suffix(32))) }
    }
    private func mix(_ secret: DH.PrivateKey, _ peer: DH.PublicKey) throws -> SymmetricKey {
        guard let dh = try? secret.sharedSecretFromKeyAgreement(with: peer) else { throw RCPError.credential }
        guard dh.withUnsafeBytes({ $0.contains { $0 != 0 } }) else { throw RCPError.credential }
        let (ck, key) = dh.withUnsafeBytes { derive(Data($0)) }
        chain = ck
        return key
    }
    private func key(for publicBytes: Data, writing: Bool) throws -> SymmetricKey {
        guard step < 2, writing == ((step == 0) == (role == .client)) else { throw RCPError.frame }
        if !writing { remoteEphemeral = try DH.PublicKey(rawRepresentation: publicBytes) }
        mixHash(publicBytes)
        defer { step += 1 }
        if step == 0 {
            if role == .client { _ = try mix(ephemeral, remote) }
            else if let other = remoteEphemeral { _ = try mix(local, other) }
            else { throw RCPError.frame }
            return try mix(local, remote)
        }
        guard let other = remoteEphemeral else { throw RCPError.frame }
        _ = try mix(ephemeral, other)
        return role == .client ? try mix(local, other) : try mix(ephemeral, remote)
    }
    func write(_ payload: Data) throws -> Data {
        let pub = ephemeral.publicKey.rawRepresentation
        let key = try key(for: pub, writing: true)
        let encrypted = try encrypt(key, payload, nonce: Data(repeating: 0, count: 12), ad: hash)
        mixHash(encrypted)
        return version + pub + encrypted
    }
    func read(_ frame: Data) throws -> Data {
        try checkHandshake(frame)
        guard isPair(frame) else { throw RCPError.frame }
        do {
            let key = try key(for: Data(frame.dropFirst(4).prefix(32)), writing: false)
            let encrypted = Data(frame.suffix(24))
            let plain = try decrypt(key, encrypted, nonce: Data(repeating: 0, count: 12), ad: hash)
            mixHash(encrypted)
            return plain
        } catch { throw RCPError.credential }
    }
    func split(limit: Int) throws -> (tx: Direction, rx: Direction) {
        guard step == 2 else { throw RCPError.frame }
        let (first, second) = derive(Data())
        let a = Direction(key: SymmetricKey(data: first), limit: limit)
        let b = Direction(key: second, limit: limit)
        return role == .client ? (a, b) : (b, a)
    }
}

final class Direction: @unchecked Sendable {
    private let lock = NSLock()
    private let key: SymmetricKey
    private let limit: Int
    private var next: UInt64 = 0
    init(key: SymmetricKey, limit: Int) { self.key = key; self.limit = limit }

    func seal(_ kind: UInt8, _ body: Data = Data()) throws -> Data {
        lock.lock(); defer { lock.unlock() }
        guard body.count <= limit else { throw RCPError.size }
        guard next < .max else { throw RCPError.frame }
        let header = recordPrefix + bigEndian(next)
        let bytes = try encrypt(key, Data([kind]) + body, nonce: header.suffix(12), ad: header)
        next += 1
        return header + bytes
    }
    func open(_ frame: Data) throws -> (UInt8, Data) {
        lock.lock(); defer { lock.unlock() }
        if isPair(frame) { throw RCPError.restarted }
        guard frame.count <= limit + 33 else { throw RCPError.size }
        guard next < .max, envelope(frame), uint(frame.dropFirst(8).prefix(8)) == next else { throw RCPError.frame }
        let header = frame.prefix(16)
        guard let plain = try? decrypt(key, frame.dropFirst(16), nonce: header.suffix(12), ad: header),
              let kind = plain.first, kind <= 6, kind == 1 || plain.count == 1 else { throw RCPError.frame }
        next += 1
        return (kind, Data(plain.dropFirst()))
    }
}
