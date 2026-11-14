import Foundation
import Network
import Observation

/// The first item of each id, in order.
nonisolated func uniqueByID<T: Identifiable>(_ items: [T]) -> [T] where T.ID: Hashable {
    var seen = Set<T.ID>()
    return items.filter { seen.insert($0.id).inserted }
}

/// A ply server that advertises itself on the LAN as _ply._tcp.
nonisolated struct DiscoveredServer: Identifiable, Hashable, Sendable {
    /// The server's id from its TXT record, else the service's name.
    let id: String
    /// What people call it: the TXT record's name, else the service's name.
    let name: String
    /// The mDNS instance name: it tells two servers with the same name apart, as in "NAS (2)".
    let service: String
    let endpoint: NWEndpoint
    let compatibility: Compatibility
    /// The name its certificate is for, when it speaks HTTPS (tls=1, host=).
    let tlsHost: String?

    init?(_ result: NWBrowser.Result) {
        guard case let .service(service, _, _, _) = result.endpoint else { return nil }
        var txt: [String: String] = [:]
        if case let .bonjour(record) = result.metadata { txt = record.dictionary }
        self.service = service
        id = txt["id"] ?? service
        name = txt["name"] ?? service
        endpoint = result.endpoint
        // A record without them is from a server older than M11.
        compatibility = Compatibility(api: Int(txt["api"] ?? "") ?? 0, apiMin: Int(txt["apiMin"] ?? "") ?? 0)
        tlsHost = txt["tls"] == "1" ? txt["host"].flatMap { $0.isEmpty ? nil : $0 } : nil
    }
}

/// Browses for ply servers with NWBrowser while it is started (docs/M11-CLIENTS.md, "Platforms").
@Observable final class Discovery {
    private(set) var servers: [DiscoveredServer] = []
    private(set) var problem: String?
    private var browser: NWBrowser?

    func start() {
        guard browser == nil else { return }
        let b = NWBrowser(for: .bonjourWithTXTRecord(type: "_ply._tcp", domain: nil), using: .tcp)
        b.browseResultsChangedHandler = { [weak self] results, _ in
            // The same service is reported once per interface it is seen on; its TXT id names it.
            let list = uniqueByID(results.compactMap(DiscoveredServer.init))
                .sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
            Task { @MainActor in self?.servers = list }
        }
        b.stateUpdateHandler = { [weak self] state in
            let problem: String? = switch state {
            case let .failed(e), let .waiting(e): "Cannot look for servers: \(e.localizedDescription)"
            default: nil
            }
            Task { @MainActor in self?.problem = problem }
        }
        b.start(queue: .main)
        browser = b
    }

    func stop() {
        browser?.cancel()
        browser = nil
    }

    /// The base URL of a discovered server: https:// and the name of its certificate when it speaks
    /// HTTPS, else http:// and the IPv4 address mDNS resolves (App Transport Security allows plain
    /// HTTP to an address, not to most names).
    static func resolve(_ server: DiscoveredServer) async throws -> URL {
        let params = NWParameters.tcp
        if let ip = params.defaultProtocolStack.internetProtocol as? NWProtocolIP.Options {
            ip.version = .v4
        }
        let connection = NWConnection(to: server.endpoint, using: params)
        let resolved = try await withCheckedThrowingContinuation { (cont: CheckedContinuation<(String, UInt16), any Error>) in
            let once = Once(cont)
            connection.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    if case let .hostPort(host, port)? = connection.currentPath?.remoteEndpoint {
                        once.resume(.success((hostString(host), port.rawValue)))
                    } else {
                        once.resume(.failure(APIError(status: 0, message: "\(server.name) has no address")))
                    }
                    connection.cancel()
                case let .failed(e), let .waiting(e):
                    once.resume(.failure(e))
                    connection.cancel()
                default:
                    break
                }
            }
            connection.start(queue: .global())
            DispatchQueue.global().asyncAfter(deadline: .now() + 5) {
                once.resume(.failure(APIError(status: 0, message: "\(server.name) did not answer")))
                connection.cancel()
            }
        }
        let host = server.tlsHost ?? resolved.0
        let scheme = server.tlsHost == nil ? "http" : "https"
        guard let url = URL(string: "\(scheme)://\(host):\(resolved.1)") else {
            throw APIError(status: 0, message: "\(server.name) has an address that is not a URL")
        }
        return url
    }

    nonisolated private static func hostString(_ host: NWEndpoint.Host) -> String {
        switch host {
        case let .ipv4(a): "\(a)".split(separator: "%").first.map(String.init) ?? "\(a)"
        case let .ipv6(a): "[\("\(a)".split(separator: "%").first.map(String.init) ?? "\(a)")]"
        case let .name(n, _): n
        @unknown default: "\(host)"
        }
    }
}

/// A continuation resumed once, whichever of its callers comes first.
nonisolated final class Once<T: Sendable>: @unchecked Sendable {
    private var cont: CheckedContinuation<T, any Error>?
    private let lock = NSLock()

    init(_ cont: CheckedContinuation<T, any Error>) { self.cont = cont }

    func resume(_ result: Result<T, any Error>) {
        lock.lock()
        let c = cont
        cont = nil
        lock.unlock()
        c?.resume(with: result)
    }
}

/// What a person types as the server's address: "192.168.1.10", "nas.local:8420" or a whole URL.
/// Without a scheme it is http, and without a port ply's own, 8420.
nonisolated enum ServerAddress {
    static let defaultPort = 8420

    static func parse(_ input: String) -> URL? {
        let typed = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !typed.isEmpty, !typed.contains(where: \.isWhitespace) else { return nil }
        let hasScheme = typed.contains("://")
        guard var c = URLComponents(string: hasScheme ? typed : "http://" + typed),
              let scheme = c.scheme?.lowercased(), scheme == "http" || scheme == "https",
              let host = c.host, !host.isEmpty
        else { return nil }
        c.scheme = scheme
        if c.port == nil && !hasScheme { c.port = defaultPort }
        c.path = ""
        c.query = nil
        c.fragment = nil
        c.user = nil
        c.password = nil
        return c.url
    }
}
