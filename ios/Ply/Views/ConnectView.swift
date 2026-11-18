import SwiftUI

/// Finds a ply server on the network, or takes its address.
struct ConnectView: View {
    @Environment(AppModel.self) private var model
    @State private var discovery = Discovery()
    @State private var address = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    if discovery.servers.isEmpty {
                        HStack(spacing: 12) {
                            ProgressView()
                            Text("Looking for ply servers…").foregroundStyle(.secondary)
                        }
                    }
                    ForEach(discovery.servers) { server in
                        Button {
                            connect { try await Discovery.resolve(server) }
                        } label: {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(server.name).foregroundStyle(.primary)
                                // Two servers can call themselves the same thing; the mDNS instance
                                // tells them apart.
                                if duplicateNames.contains(server.name) {
                                    Text(server.service).font(.caption2).foregroundStyle(.secondary)
                                }
                                if let problem = server.compatibility.problem {
                                    Text(problem).font(.caption).foregroundStyle(.orange)
                                }
                            }
                        }
                    }
                } header: {
                    Text("On this network")
                } footer: {
                    if let problem = discovery.problem { Text(problem) }
                }

                Section {
                    TextField("192.168.1.10 or nas.local:8420", text: $address)
                        #if os(iOS)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .textInputAutocapitalization(.never)
                        #endif
                        .autocorrectionDisabled()
                        #if os(iOS)
                        .submitLabel(.go)
                        #endif
                        .onSubmit(connectTyped)
                    Button("Connect", action: connectTyped)
                        .disabled(ServerAddress.parse(address) == nil)
                } header: {
                    Text("Address")
                } footer: {
                    Text("Without a port, ply's own (\(String(ServerAddress.defaultPort))) is used.")
                }

                if let error {
                    Section {
                        Text(error).foregroundStyle(.red)
                    }
                }
            }
            .plyNoBlurHeader()
            .navigationTitle("Connect to ply")
            .disabled(busy)
            .overlay { if busy { ProgressView().controlSize(.large) } }
        }
        .onAppear { discovery.start() }
        .onDisappear { discovery.stop() }
    }

    private var duplicateNames: Set<String> {
        Set(Dictionary(grouping: discovery.servers, by: \.name).filter { $0.value.count > 1 }.keys)
    }

    private func connectTyped() {
        guard let url = ServerAddress.parse(address) else { return }
        connect { url }
    }

    private func connect(_ base: @escaping () async throws -> URL) {
        busy = true
        error = nil
        Task {
            do {
                try await model.connect(to: try await base())
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}

/// Pairs this device with a server that asks for a credential: the owner shows a code in the web
/// UI (Settings › Devices › Pair a device), and it is typed here.
struct PairView: View {
    let pending: PendingServer
    @Environment(AppModel.self) private var model
    @State private var code = ""
    @State private var busy = false
    @State private var error: String?

    private var digits: String { code.filter(\.isNumber) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("6-digit code", text: $code)
                        #if os(iOS)
                        .keyboardType(.numberPad)
                        .textContentType(.oneTimeCode)
                        #endif
                        .font(.title2.monospacedDigit())
                    Button("Pair", action: pair)
                        .disabled(digits.count != 6)
                } header: {
                    Text("Pair with \(pending.info.name)")
                } footer: {
                    Text("On ply's web page, open Settings › Devices › Pair a device, and type the code it shows.")
                }
                if let error {
                    Section { Text(error).foregroundStyle(.red) }
                }
                Section {
                    Button("Use another server") { model.chooseServer() }
                }
            }
            .plyNoBlurHeader()
            .navigationTitle("Pair this device")
            .disabled(busy)
            .overlay { if busy { ProgressView().controlSize(.large) } }
        }
    }

    private func pair() {
        busy = true
        error = nil
        let code = digits
        Task {
            do {
                try await model.pair(pending, code: code)
            } catch {
                self.error = error.localizedDescription
                self.code = ""
            }
            busy = false
        }
    }
}

/// The server of last time did not answer, or answered that its API version and this app's do not
/// overlap. Those are different problems, and only one of them has "try again".
struct ConnectionProblemView: View {
    enum Kind: Equatable {
        case unreachable
        case incompatible
    }

    let saved: SavedServer
    let message: String
    let kind: Kind
    @Environment(AppModel.self) private var model

    var body: some View {
        ContentUnavailableView {
            Label(title, systemImage: icon)
        } description: {
            Text(message)
        } actions: {
            if kind == .unreachable {
                Button("Try again") { Task { await model.retry(saved) } }
                    .buttonStyle(.borderedProminent)
            }
            Button("Choose another server") { model.chooseServer() }
        }
    }

    private var title: String {
        switch kind {
        case .unreachable: "Cannot reach \(saved.name)"
        case .incompatible: "\(saved.name) cannot be used"
        }
    }

    private var icon: String {
        switch kind {
        case .unreachable: "wifi.exclamationmark"
        case .incompatible: "exclamationmark.triangle"
        }
    }
}
