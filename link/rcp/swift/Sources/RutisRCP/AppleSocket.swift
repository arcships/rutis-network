import CFNetwork
import CryptoKit
import Foundation
import Network

/// Pull one WebSocket message at a time. URLSession fixes its buffer ceiling at
/// upgrade time, while RCP changes its ceiling only after authenticating READY.
final class AppleSocket: RCPBinarySocket, @unchecked Sendable {
    private let connection: NWConnection
    private let requestTarget: String
    private let authority: String
    // Only the single upgrade/receive task accesses these, including limit changes.
    private var remainder = Data()
    private var limit = 128

    init(_ address: URL) throws {
        guard let url = URLComponents(url: address, resolvingAgainstBaseURL: false),
              let hostname = address.host, !hostname.isEmpty,
              url.user == nil, url.password == nil, url.fragment == nil else { throw RCPError.credential }
        let literal = hostname.trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        var ipv4 = in_addr(), ipv6 = in6_addr()
        let loopback = (inet_pton(AF_INET, literal, &ipv4) == 1 && UInt32(bigEndian: ipv4.s_addr) >> 24 == 127) ||
            (inet_pton(AF_INET6, literal, &ipv6) == 1 && withUnsafeBytes(of: ipv6) { $0.dropLast().allSatisfy { $0 == 0 } && $0.last == 1 })
        let secure = url.scheme == "wss"
        guard secure || (url.scheme == "ws" && loopback),
              let port = UInt16(exactly: url.port ?? (secure ? 443 : 80)), port > 0,
              hostname.utf8.allSatisfy({ (33...126).contains($0) }) else { throw RCPError.credential }

        let host = literal.contains(":") ? "[\(literal)]" : literal
        authority = host + (url.port.map { ":\($0)" } ?? "")
        requestTarget = (url.percentEncodedPath.isEmpty ? "/" : url.percentEncodedPath) +
            (url.percentEncodedQuery.map { "?\($0)" } ?? "")
        connection = NWConnection(host: NWEndpoint.Host(literal), port: NWEndpoint.Port(rawValue: port)!,
                                  using: secure ? .tls : .tcp)
    }

    func upgrade() async throws {
        connection.start(queue: DispatchQueue(label: "arcships.rcp.websocket"))
        let nonce = Data((0..<16).map { _ in UInt8.random(in: .min ... .max) }).base64EncodedString()
        let headers = ["GET \(requestTarget) HTTP/1.1", "Host: \(authority)", "Upgrade: websocket",
                       "Connection: Upgrade", "Sec-WebSocket-Version: 13", "Sec-WebSocket-Key: \(nonce)", "", ""]
        try await sendBytes(Data(headers.joined(separator: "\r\n").utf8))
        let delimiter = Data("\r\n\r\n".utf8)
        var bytes = Data()
        while bytes.count < 16_384 {
            let scanFrom = max(0, bytes.count - delimiter.count + 1)
            bytes.append(try await receiveBytes(upTo: min(128, 16_384 - bytes.count)))
            guard let end = bytes.range(of: delimiter, in: scanFrom..<bytes.count) else { continue }
            let header = bytes[..<end.upperBound]
            let response = CFHTTPMessageCreateEmpty(nil, false).takeRetainedValue()
            guard header.withUnsafeBytes({ CFHTTPMessageAppendBytes(response, $0.bindMemory(to: UInt8.self).baseAddress!, $0.count) }),
                  CFHTTPMessageIsHeaderComplete(response) else { throw RCPError.frame }
            switch CFHTTPMessageGetResponseStatusCode(response) {
            case 101: break
            case 401, 403: throw RCPError.credential
            case 410: throw RCPError.expired
            case 404: throw RCPError.offline
            case 429, 502, 503, 504: throw RCPError.unavailable
            default: throw RCPError.refused
            }
            func field(_ name: String) -> String? {
                CFHTTPMessageCopyHeaderFieldValue(response, name as CFString)?.takeRetainedValue() as String?
            }
            let tokens = (field("Connection") ?? "").lowercased().split(separator: ",")
            let challenge = Data((nonce + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").utf8)
            let accept = Data(Insecure.SHA1.hash(data: challenge)).base64EncodedString()
            guard field("Upgrade")?.lowercased() == "websocket",
                  tokens.contains(where: { $0.trimmingCharacters(in: .whitespaces) == "upgrade" }),
                  field("Sec-WebSocket-Accept") == accept, field("Sec-WebSocket-Extensions") == nil,
                  field("Sec-WebSocket-Protocol") == nil else { throw RCPError.frame }
            remainder = Data(bytes[end.upperBound...])
            return
        }
        throw RCPError.frame
    }

    func setReadLimit(_ bytes: Int) { limit = bytes }

    private func receiveBytes(upTo count: Int) async throws -> Data {
        try await withCheckedThrowingContinuation { continuation in
            connection.receive(minimumIncompleteLength: 1, maximumLength: count) { data, _, _, error in
                if let data, !data.isEmpty { continuation.resume(returning: data) }
                else if error == .posix(.EMSGSIZE) { continuation.resume(throwing: RCPError.size) }
                else { continuation.resume(throwing: RCPError.transport) }
            }
        }
    }

    private func take(_ count: Int) async throws -> Data {
        var bytes = Data(remainder.prefix(count))
        remainder.removeFirst(bytes.count)
        while bytes.count < count {
            bytes.append(try await receiveBytes(upTo: min(count - bytes.count, 65_536)))
        }
        return bytes
    }

    func read() async throws -> Data {
        var message = Data()
        var fragmented = false
        while true {
            let header = try await take(2)
            let fin = header[0] & 0x80 != 0, opcode = header[0] & 0x0f
            guard header[0] & 0x70 == 0, header[1] & 0x80 == 0 else { throw RCPError.frame }
            var length = UInt64(header[1])
            if opcode >= 8 {
                guard fin, length <= 125 else { throw RCPError.frame }
                let payload = try await take(Int(length))
                switch opcode {
                case 8:
                    guard payload.count != 1 else { throw RCPError.frame }
                    let tooLarge = payload.starts(with: [0x03, 0xf1]) // WebSocket close 1009
                    try? await sendFrame(8, payload)
                    throw tooLarge ? RCPError.size : RCPError.transport
                case 9: try await sendFrame(10, payload)
                case 10: break
                default: throw RCPError.frame
                }
                continue
            }
            guard opcode == (fragmented ? 0 : 2) else { throw RCPError.frame }
            if length >= 126 {
                let wide = length == 127
                length = try await take(wide ? 8 : 2).reduce(0) { ($0 << 8) | UInt64($1) }
                guard wide ? (65_536...UInt64(Int64.max)).contains(length) : length >= 126 else { throw RCPError.frame }
            }
            guard length <= UInt64(limit - message.count) else { throw RCPError.size }
            message.append(try await take(Int(length)))
            if fin { return message }
            fragmented = true
        }
    }

    private func sendBytes(_ bytes: Data) async throws {
        try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
            connection.send(content: bytes, completion: .contentProcessed { error in
                if error != nil { continuation.resume(throwing: RCPError.transport) }
                else { continuation.resume() }
            })
        }
    }

    private func sendFrame(_ opcode: UInt8, _ bytes: Data) async throws {
        var frame = Data([0x80 | opcode])
        switch bytes.count {
        case 0..<126: frame.append(0x80 | UInt8(bytes.count))
        case 126...65_535:
            frame.append(0xfe)
            withUnsafeBytes(of: UInt16(bytes.count).bigEndian) { frame.append(contentsOf: $0) }
        default:
            frame.append(0xff)
            withUnsafeBytes(of: UInt64(bytes.count).bigEndian) { frame.append(contentsOf: $0) }
        }
        let mask = (0..<4).map { _ in UInt8.random(in: .min ... .max) }
        frame.append(contentsOf: mask)
        let start = frame.count
        frame.append(bytes)
        frame.withUnsafeMutableBytes { (buffer: UnsafeMutableRawBufferPointer) in
            for offset in 0..<bytes.count {
                buffer[start + offset] ^= mask[offset & 3]
            }
        }
        try await sendBytes(frame)
    }

    func write(_ bytes: Data) async throws { try await sendFrame(2, bytes) }
    func abort() { connection.cancel() }
}
