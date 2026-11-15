import Foundation
import Observation
import SwiftUI

/// A server that answered and needs this device paired before anything else.
struct PendingServer {
    let base: URL
    let info: ServerInfo
}

/// The server this app is connected to, ready to browse and play.
@Observable final class Connection {
    let base: URL
    let info: ServerInfo
    let api: APIClient
    let library: LibraryModel
    let browse: BrowseModel

    init(base: URL, info: ServerInfo, api: APIClient) {
        self.base = base
        self.info = info
        self.api = api
        library = LibraryModel(api: api)
        browse = BrowseModel(api: api)
    }

    /// The device has a token of its own (the server asks for one).
    var paired: Bool { api.token != nil }
}

/// Where the app stands with its server.
enum Phase {
    case starting
    case choosingServer
    case unreachable(SavedServer, String)
    /// The server answered, but its API version and this app's do not overlap.
    case incompatible(SavedServer, String)
    case pairing(PendingServer)
    case ready(Connection)
}

/// What to play: an item, where to start (nil: where its progress says), the tracks, and an
/// AirPlay target to route the play to.
struct PlayRequest {
    let itemId: Int64
    var startSec: Double?
    var audioIndex: Int?
    var subtitle: SubtitleChoice?
    var receiver: Receiver?
}

/// The app's state: the connection to its server (docs/M11-CLIENTS.md), the live feed, remote
/// control, and the play in progress.
@Observable final class AppModel {
    private(set) var phase: Phase = .starting
    /// A message to show once.
    var alert: String?

    private var feed: EventFeed?
    private var playback: PlaybackController?
    /// The window showing the play, when one is: one play at a time, in the window that started it.
    private weak var shownIn: PlaybackScene?
    /// The windows that can show a play: held weakly, so a window the person closed is not kept alive.
    private var windows = PlaybackWindows()
    private var handledCommands: Set<Int64> = []
    /// The newest command this device acted on for this server, across launches.
    private var lastHandledSeq: Int64 = 0

    var connection: Connection? {
        if case let .ready(c) = phase { c } else { nil }
    }

    // MARK: connecting

    /// Connects to the server of last time, if there is one.
    func start() async {
        guard case .starting = phase else { return }
        guard let saved = SavedServer.load() else {
            phase = .choosingServer
            return
        }
        await retry(saved)
    }

    func retry(_ saved: SavedServer) async {
        phase = .starting
        do {
            try await connect(to: saved.base)
        } catch {
            switch ConnectFailure.classify(error) {
            case let .incompatible(message): phase = .incompatible(saved, message)
            case let .unreachable(message): phase = .unreachable(saved, message)
            }
        }
    }

    /// Leaves the server: the saved one is forgotten too, so a relaunch does not take the person
    /// back to it. Its token stays unless "Forget this server" is used, so reconnecting needs no code.
    func chooseServer() {
        teardown()
        SavedServer.clear()
        phase = .choosingServer
    }

    /// Asks the server at base who it is, checks that the versions match, then pairs (when the
    /// server needs a credential and this device has none for it) or opens the connection.
    func connect(to base: URL) async throws {
        let info = try await APIClient(base: base, token: nil, device: .live).info()
        if let problem = Compatibility(api: info.api, apiMin: info.apiMin).problem {
            throw CompatibilityProblem(problem: problem)
        }
        let token = Keychain.token(for: info.id)
        if info.authRequired && token == nil {
            phase = .pairing(PendingServer(base: base, info: info))
            return
        }
        try await open(base: base, info: info, token: info.authRequired ? token : nil)
    }

    /// Pairs with the code the owner was shown in Settings › Devices.
    func pair(_ pending: PendingServer, code: String) async throws {
        let token = try await APIClient(base: pending.base, token: nil, device: .live).pair(code: code)
        Keychain.setToken(token, for: pending.info.id)
        try await open(base: pending.base, info: pending.info, token: token)
    }

    private func open(base: URL, info: ServerInfo, token: String?) async throws {
        let api = APIClient(base: base, token: token, device: .live)
        do {
            try await api.registerDevice()
        } catch let e as APIError where token != nil && (e.status == 401 || e.status == 403) {
            // Revoked while the app was away (401), or paired for another device id (403).
            Keychain.setToken(nil, for: info.id)
            phase = .pairing(PendingServer(base: base, info: info))
            return
        }
        api.onUnauthorized = { [weak self] in self?.revoked() }
        SavedServer(id: info.id, name: info.name, base: base).save()
        let connection = Connection(base: base, info: info, api: api)
        teardown()
        lastHandledSeq = CommandHistory.lastHandled(for: info.id)
        phase = .ready(connection)
        let feed = EventFeed(api: api, topics: ["library", "commands"]) { [weak self] in self?.handle($0) }
        self.feed = feed
        feed.start()
        await connection.library.reload()
    }

    /// Forgets the server and this device's token for it.
    func disconnect() {
        if let c = connection {
            Keychain.setToken(nil, for: c.info.id)
        }
        SavedServer.clear()
        chooseServer()
    }

    private func revoked() {
        guard let c = connection else { return }
        Keychain.setToken(nil, for: c.info.id)
        teardown()
        phase = .pairing(PendingServer(base: c.base, info: c.info))
        alert = "This device is no longer paired with \(c.info.name). Pair it again to go on."
    }

    private func teardown() {
        feed?.stop()
        feed = nil
        connection?.library.cancel()
        endPlayback(.closed)
        handledCommands.removeAll()
        lastHandledSeq = 0
    }

    /// The feed is closed in the background, where iOS would suspend it anyway, and opened again on
    /// return: the server replays what was missed (Last-Event-ID), or sends a snapshot.
    func scenePhaseChanged(_ scene: ScenePhase) {
        switch scene {
        case .background: feed?.stop()
        case .active: feed?.start()
        default: break
        }
    }

    // MARK: the feed and remote control

    private func handle(_ event: SSEEvent) {
        guard let c = connection else { return }
        let data = Data(event.data.utf8)
        switch event.type {
        case "library":
            c.library.scheduleReload()
        case "snapshot":
            // A snapshot also means "fetch the library again".
            c.library.scheduleReload()
            let snapshot: Snapshot? = try? APIClient.decode(data)
            for command in snapshot?.commands ?? [] { perform(command, on: c) }
        case "command.acked":
            break
        case let type where type.hasPrefix("command."):
            if let e: CommandEvent = try? APIClient.decode(data) { perform(e.command, on: c) }
        default:
            break
        }
    }

    /// Does what another device asked (docs/M11-CLIENTS.md, "Remote control") and answers it. A
    /// command that has expired is dropped: a phone woken hours later must not start a film.
    private func perform(_ command: Command, on c: Connection) {
        // A device acts on a seq once, across launches too: an unacked command can come again in a
        // snapshot (docs/M11-CLIENTS.md, "Remote control").
        guard command.expiresAt > Date(),
              command.seq > lastHandledSeq,
              handledCommands.insert(command.seq).inserted else { return }
        lastHandledSeq = command.seq
        CommandHistory.setLastHandled(command.seq, for: c.info.id)
        var failure: String?
        /// A play is answered when it has started (or failed to), not before.
        var answered = false
        let nothing = "Nothing is playing"
        switch command.type {
        case "play":
            if let id = command.itemId {
                // A play from another device lands in the window that started the last one, or the
                // newest window there is. Only the play this command started may answer it.
                let before = playback
                let window = shownIn ?? lastWindow
                if let window {
                    play(PlayRequest(itemId: id, startSec: command.positionSec), in: window)
                }
                let result = PlayCommandResult.after(hasServer: connection != nil, hadWindow: window != nil,
                                                     started: playback !== before)
                if case .started = result, let p = playback {
                    p.onStart = { [weak c] error in
                        guard let c else { return }
                        Task { try? await c.api.ack(command.seq, ok: error == nil, error: error) }
                    }
                    answered = true
                } else {
                    failure = result.failure
                }
            } else {
                failure = "play needs an itemId"
            }
        case "pause":
            if let p = playback { p.pause() } else { failure = nothing }
        case "resume":
            if let p = playback { p.resume() } else { failure = nothing }
        case "seek":
            if let p = playback, let at = command.positionSec { p.seek(to: at) } else { failure = nothing }
        case "stop":
            if playback != nil { endPlayback(.closed) } else { failure = nothing }
        default:
            failure = "Unknown command \(command.type)"
        }
        if !answered {
            Task { try? await c.api.ack(command.seq, ok: failure == nil, error: failure) }
        }
    }

    // MARK: playback

    /// A window says it is here, so that a play from another device lands in one (the newest window
    /// to appear).
    func register(_ scene: PlaybackScene) {
        windows.add(scene)
    }

    /// The window went away (its scene disconnected, or it left the screen). A play it was showing ends
    /// here — the player is closed and its progress sent — but it is not dismissed: there is no window
    /// left to dismiss it into. Called more than once, it does nothing the second time.
    func unregister(_ scene: PlaybackScene) {
        windows.remove(scene)
        scene.windowScene = nil
        guard scene.presentation.shown else { return }
        scene.forget()
        guard let p = playback, shownIn === scene else { return }
        playback = nil
        shownIn = nil
        p.close()
        connection?.library.scheduleReload()
    }

    /// Whether a window is still there (a play that was to continue in it may find it gone).
    private func isLive(_ scene: PlaybackScene) -> Bool {
        windows.contains(scene)
    }

    /// The newest window there is, for a play nobody started.
    private var lastWindow: PlaybackScene? { windows.newest as? PlaybackScene }

    func play(_ request: PlayRequest, in scene: PlaybackScene) {
        guard let c = connection else { return }
        if playback != nil { endPlayback(.closed) } // one play at a time: the first one goes, where it was
        let p = PlaybackController(api: c.api, request: request)
        playback = p
        shownIn = scene
        p.onEnd = { [weak self, weak p] end in
            guard let self, let p, self.playback === p else { return }
            self.endPlayback(end)
        }
        scene.present(p) { [weak self, weak p] in
            guard let self, let p, self.playback === p else { return }
            self.endPlayback(.closed)
        }
        Task { await p.start() }
    }

    private func endPlayback(_ end: PlaybackEnd) {
        guard let p = playback else { return }
        playback = nil
        // The window that showed it takes it down, and the next episode, if any, plays there.
        let scene = shownIn
        shownIn = nil
        let detail = p.itemDetail
        p.close()
        connection?.library.scheduleReload() // its progress, Continue Watching
        let message: String? = switch end {
        case let .failed(m): m
        case .stoppedElsewhere: "Playback was stopped from another device."
        case .finished, .closed: nil
        }
        scene?.dismiss { [weak self] in
            if let message {
                self?.alert = message
            } else if case .finished = end, let detail, detail.item.isEpisode {
                self?.autoplayNextEpisode(after: detail, in: scene)
            }
        }
    }

    private func autoplayNextEpisode(after current: ItemDetail, in scene: PlaybackScene?) {
        guard let show = current.item.show, !show.isEmpty, let scene, let c = connection else { return }
        Task { @MainActor [weak self] in
            guard let self, self.connection === c, self.isLive(scene) else { return }
            do {
                let episodes = try await c.api.episodes(show: show)
                guard let next = NextEpisodeSelection.after(current.item, in: episodes) else { return }
                self.play(PlayRequest(itemId: next.id, startSec: 0), in: scene)
            } catch {
                // If the next episode cannot be fetched, stay on the current page.
            }
        }
    }
}
