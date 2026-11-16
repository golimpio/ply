import AVFoundation
import Foundation
import MediaPlayer
import Observation
import UIKit

enum PlaybackEnd {
    /// Played to the end.
    case finished
    /// Closed here, or stopped by a command.
    case closed
    /// Someone stopped the play from another device (410): it must not start again on its own.
    case stoppedElsewhere
    case failed(String)
}

/// One play of one item (docs/M11-CLIENTS.md, "Devices"): asks the server for it, keeps its lease
/// with heartbeats, reports progress, and starts it again where it was when the server lost it.
///
/// Progress is reported only once the player has reached where the stream was meant to start: before
/// that it holds no position that means anything, and a report of 0 would erase the resume point.
@Observable final class PlaybackController {
    let player = AVPlayer()
    /// The text subtitle showing now, drawn over the video.
    private(set) var subtitleText: String?
    /// Whether that text subtitle is still downloading, and why it could not be.
    private(set) var subtitleLoading = false
    private(set) var subtitleError: String?
    var onEnd: ((PlaybackEnd) -> Void)?
    /// Called once: nil when the play is ready and playing, or why it did not start. A remote
    /// `play` command is answered with this, not before it is known.
    var onStart: ((String?) -> Void)?
    private var startReported = false

    private let api: APIClient
    private var request: PlayRequest
    private var item: ItemDetail?
    var itemDetail: ItemDetail? { item }
    var availableAudioStreams: [MediaStream] { item?.audioStreams ?? [] }
    var availableSubtitles: [(Subtitle, SubtitleChoice)] {
        (item?.subtitles ?? []).compactMap { s in s.choice.map { (s, $0) } }
    }
    var activeAudioIndex: Int? {
        request.audioIndex
            ?? availableAudioStreams.first(where: { $0.default == true })?.index
            ?? availableAudioStreams.first?.index
    }
    var selectedSubtitle: SubtitleChoice? { request.subtitle }
    private var sessionID: String?
    /// Position the current stream was asked to start at, and the last that counted.
    private var resumeAt = 0.0
    private var armed = false
    private var lastSent = -1.0
    /// New sessions started in a row without playback getting going.
    private var restarts = 0
    private var generation = 0
    private var closed = false
    /// Whether the person (or a command) wants it playing: a new stream starts paused when not.
    private var wantsPlay = true
    /// A seek asked for before the stream was ready, applied once it is.
    private var pendingSeek: Double?
    @ObservationIgnored private var subtitles = WebVTT()

    private var ticker: Task<Void, Never>?
    private var subtitleTask: Task<Void, Never>?
    private var timeObserver: Any?
    private var statusObservation: NSKeyValueObservation?
    private var externalPlaybackObservation: NSKeyValueObservation?
    /// The wait for the current item to be ready, and what it watches.
    private var readiness: Once<Void>?
    private var readinessObservation: NSKeyValueObservation?
    private var notifications: [any NSObjectProtocol] = []
    private var activeProfile: String = "avplayer"
    private var nowPlayingArtwork: MPMediaItemArtwork?
    private var remoteCommandTargets: [(command: MPRemoteCommand, target: Any)] = []

    /// A heartbeat every 10 s, paused or not: a paused player asks for nothing, and without beats the
    /// server would take the play for an abandoned one. Progress goes with it.
    private static let tick: Duration = .seconds(10)
    private static let maxRestarts = 2
    /// How close to where a stream should start counts as there (a seek lands on a keyframe).
    private static let arrived = 3.0

    init(api: APIClient, request: PlayRequest) {
        self.api = api
        self.request = request
        player.allowsExternalPlayback = true
    }

    func start() async {
        observe()
        do {
            let item = try await api.item(request.itemId)
            guard !closed else { return }
            self.item = item
            setupRemoteCommands()
            loadArtwork(for: item)
            let plan = PlaybackPlanning.plan(subtitle: request.subtitle, receiver: request.receiver,
                                            externalPlayback: player.isExternalPlaybackActive)
            if plan.drawsSubtitles, case let .text(_, url) = request.subtitle {
                subtitleTask = Task { [weak self] in await self?.loadSubtitles(url) }
            }
            await open(at: request.startSec ?? item.item.resumeSec)
        } catch {
            fail(error)
        }
    }

    // MARK: control

    func pause() {
        wantsPlay = false
        player.pause()
    }

    func resume() {
        wantsPlay = true
        if armed { player.play() }
    }

    func seek(to seconds: Double) {
        guard armed else {
            pendingSeek = max(0, seconds)
            return
        }
        player.seek(to: CMTime(seconds: max(0, seconds), preferredTimescale: 600)) { [weak self] _ in
            Task { @MainActor in
                self?.updateNowPlayingInfo()
                self?.beat()
            }
        }
    }

    /// Switches to another audio stream, re-opening the server session at the current position.
    func selectAudio(_ index: Int?) async {
        guard !closed, index != activeAudioIndex else { return }
        let at = position
        request.audioIndex = index
        await open(at: at)
    }

    /// Switches subtitles: text WebVTT tracks swap instantaneously; bitmap burn-in or AirPlay renditions re-open.
    func selectSubtitle(_ choice: SubtitleChoice?) async {
        guard !closed, choice != request.subtitle else { return }
        let previous = request.subtitle
        request.subtitle = choice

        if PlaybackPlanning.canSwapSubtitleInPlace(
            from: previous,
            to: choice,
            receiver: request.receiver,
            externalPlayback: player.isExternalPlaybackActive
        ) {
            subtitleTask?.cancel()
            subtitleTask = nil
            subtitles = WebVTT()
            subtitleText = nil
            subtitleError = nil
            subtitleLoading = false
            if case let .text(_, url) = choice {
                subtitleTask = Task { [weak self] in await self?.loadSubtitles(url) }
            }
            return
        }

        let at = position
        await open(at: at)
    }

    /// Ends the play: reports where it got to and lets the server go of its session.
    func close() {
        guard !closed else { return }
        if !startReported { reportStart("The play was closed before it started.") }
        report()
        closed = true
        ticker?.cancel()
        subtitleTask?.cancel()
        readiness?.resume(.failure(CancellationError()))
        if let timeObserver { player.removeTimeObserver(timeObserver) }
        timeObserver = nil
        statusObservation = nil
        externalPlaybackObservation = nil
        clearRemoteCommands()
        MPNowPlayingInfoCenter.default().nowPlayingInfo = nil
        notifications.forEach(NotificationCenter.default.removeObserver)
        notifications = []
        player.pause()
        player.replaceCurrentItem(with: nil)
        release()
    }

    // MARK: the stream

    /// Asks for a play starting at `start` and plays it; whatever played before is let go.
    private func open(at start: Double) async {
        generation += 1
        let run = generation
        readiness?.resume(.failure(CancellationError()))
        report()
        release()
        resumeAt = start
        armed = false
        lastSent = -1
        do {
            // An AirPlay play uses the profile built for the TVs (MPEG-TS HLS, which an older Apple
            // TV plays); a text subtitle of it is a rendition (`subtitleKey`), shown by the TV, not
            // drawn here. A play that is already on AirPlay when it is restarted keeps that profile.
            let plan = PlaybackPlanning.plan(subtitle: request.subtitle, receiver: request.receiver,
                                            externalPlayback: player.isExternalPlaybackActive)
            activeProfile = plan.profile
            if !plan.drawsSubtitles {
                subtitleTask?.cancel()
                subtitleTask = nil
                subtitleText = nil
                subtitleLoading = false
                subtitleError = nil
            }
            let pb = try await api.playback(request.itemId, startSec: start, audioIndex: request.audioIndex,
                                            subtitleIndex: plan.subtitleIndex, subtitleKey: plan.subtitleKey,
                                            receiver: plan.receiver, profile: plan.profile)
            guard run == generation, !closed else {
                if let sid = pb.sessionId { Task { [api] in await api.deleteSession(sid) } }
                return
            }
            guard let path = pb.url, let url = api.url(path) else {
                throw APIError(status: 0, message: pb.reasons?.last ?? "This item cannot be played here.")
            }
            sessionID = pb.sessionId
            let playerItem = AVPlayerItem(url: url)
            if let d = item {
                configureExternalMetadata(for: playerItem, detail: d)
            }
            player.replaceCurrentItem(with: playerItem)
            startTicker()
            try await ready(playerItem)
            updateNowPlayingInfo()
            // A file starts at 0; an HLS stream already starts at `start` on the file's timeline.
            let target = pendingSeek ?? start
            pendingSeek = nil
            if abs(player.currentTime().seconds - target) > Self.arrived {
                await player.seek(to: CMTime(seconds: target, preferredTimescale: 600))
            }
            guard run == generation, !closed else { return }
            armed = true
            restarts = 0
            if wantsPlay { player.play() } else { beat() }
            reportStart(nil)
        } catch let e as APIError where e.status == 410 {
            // A newer ask of this device's took the play's place: this one is over.
            if run == generation { finish(.stoppedElsewhere) }
        } catch is CancellationError {
            return
        } catch {
            if run == generation { fail(error) }
        }
    }

    /// Waits for the item to be ready to play; throws its error, or CancellationError when the
    /// play moved on first.
    private func ready(_ item: AVPlayerItem) async throws {
        var mine: Once<Void>?
        defer {
            // Only this wait's: a newer stream may have started its own since.
            if let mine, readiness === mine {
                readiness = nil
                readinessObservation = nil
            }
        }
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, any Error>) in
            let once = Once(cont)
            mine = once
            readiness = once
            readinessObservation = item.observe(\.status, options: [.initial, .new]) { item, _ in
                switch item.status {
                case .readyToPlay:
                    once.resume(.success(()))
                case .failed:
                    once.resume(.failure(item.error ?? APIError(status: 0, message: "The video could not be played.")))
                default:
                    break
                }
            }
        }
    }

    /// Lets the server go of the current session (an HLS session, or a direct play).
    private func release() {
        ticker?.cancel()
        guard let sid = sessionID else { return }
        sessionID = nil
        Task { [api] in await api.deleteSession(sid) }
    }

    /// The server lost the session (404), or the stream broke: a new one where playback is, a few
    /// times in a row at most.
    private func restart() {
        guard !closed else { return }
        restarts += 1
        guard restarts <= Self.maxRestarts else {
            fail(APIError(status: 0, message: "The server keeps losing this play."))
            return
        }
        let at = position
        Task { await open(at: at) }
    }

    /// Where the next stream should start: the live position once the stream counts, else where it
    /// was asked to start.
    private var position: Double {
        let t = player.currentTime().seconds
        return armed && t.isFinite ? t : resumeAt
    }

    // MARK: heartbeats and progress

    private func startTicker() {
        ticker?.cancel()
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.tick)
                guard !Task.isCancelled, let self else { return }
                self.report()
                self.beat()
            }
        }
    }

    /// Keeps the session alive. A 404 means the server lost it: a new one where playback is. A 410
    /// means someone stopped it on purpose: it must not start again on its own. After a media
    /// failure (`afterFailure`), which looks the same either way, the beat tells which it was, and
    /// a session still alive is started again too.
    private func beat(afterFailure: Bool = false) {
        guard let sid = sessionID else {
            if afterFailure { restart() }
            return
        }
        let paused = player.timeControlStatus == .paused
        let wireless = player.isExternalPlaybackActive
        let at = position
        Task { [weak self, api] in
            var status = 200
            do {
                try await api.heartbeat(sid, positionSec: at, paused: paused, wireless: wireless)
            } catch let e as APIError {
                status = e.status
            } catch {
                status = 0
            }
            // Only the session this beat was for: beats in flight may all come back 404.
            guard let self, self.sessionID == sid else { return }
            switch status {
            case 410:
                self.sessionID = nil
                self.finish(.stoppedElsewhere)
            case 404:
                self.sessionID = nil
                self.restart()
            default:
                if afterFailure { self.restart() } // else left to the next beat
            }
        }
    }

    /// Tells the server where playback is, if that is a position that means something.
    private func report() {
        guard armed, !closed, let item else { return }
        let t = player.currentTime().seconds
        guard t.isFinite, t != lastSent else { return }
        lastSent = t
        resumeAt = t
        let streamDuration = player.currentItem?.duration.seconds ?? 0
        let duration = item.item.durationSec > 0 ? item.item.durationSec : (streamDuration.isFinite ? streamDuration : 0)
        let id = request.itemId
        Task { [api] in try? await api.putProgress(id, positionSec: t, durationSec: duration) }
    }

    // MARK: observing the player

    private func observe() {
        timeObserver = player.addPeriodicTimeObserver(forInterval: CMTime(value: 1, timescale: 4), queue: .main) { [weak self] time in
            MainActor.assumeIsolated { self?.showSubtitle(at: time.seconds) }
        }
        // On pause and play: a beat at once, and the position on pause. A pause or play of the
        // person's is what a new stream (after a lost session) starts as; a stream that broke or
        // ended is not a pause of theirs.
        statusObservation = player.observe(\.timeControlStatus, options: [.new]) { [weak self] _, _ in
            Task { @MainActor in
                guard let self else { return }
                let status = self.player.timeControlStatus
                if self.armed, let item = self.player.currentItem, item.error == nil,
                   item.currentTime() < item.duration || !item.duration.isNumeric {
                    if status == .playing { self.wantsPlay = true }
                    if status == .paused { self.wantsPlay = false }
                }
                if status == .paused { self.report() }
                self.updateNowPlayingInfo()
                self.beat()
            }
        }
        externalPlaybackObservation = player.observe(\.isExternalPlaybackActive, options: [.old, .new]) { [weak self] player, change in
            Task { @MainActor in
                guard let self, !self.closed else { return }
                let isExternal = player.isExternalPlaybackActive
                let wasExternal = change.oldValue ?? false
                guard isExternal != wasExternal else { return }
                let plan = PlaybackPlanning.plan(subtitle: self.request.subtitle, receiver: self.request.receiver,
                                                externalPlayback: isExternal)
                if plan.profile != self.activeProfile {
                    if plan.drawsSubtitles, case let .text(_, url) = self.request.subtitle {
                        if self.subtitleTask == nil {
                            self.subtitleTask = Task { [weak self] in await self?.loadSubtitles(url) }
                        }
                    } else if !plan.drawsSubtitles {
                        self.subtitleTask?.cancel()
                        self.subtitleTask = nil
                        self.subtitleText = nil
                    }
                    await self.open(at: self.position)
                }
            }
        }
        let center = NotificationCenter.default
        notifications = [
            center.addObserver(forName: AVPlayerItem.didPlayToEndTimeNotification, object: nil, queue: .main) { [weak self] note in
                let item = note.object as? AVPlayerItem
                MainActor.assumeIsolated {
                    guard let self, item === self.player.currentItem else { return }
                    self.report()
                    self.finish(.finished)
                }
            },
            center.addObserver(forName: AVPlayerItem.failedToPlayToEndTimeNotification, object: nil, queue: .main) { [weak self] note in
                let item = note.object as? AVPlayerItem
                MainActor.assumeIsolated {
                    guard let self, item === self.player.currentItem else { return }
                    self.report()
                    self.beat(afterFailure: true)
                }
            },
        ]
    }

    /// Reads the track as it arrives: the server writes an embedded track's cues while it extracts
    /// them, which over a slow share takes minutes. Each cue shows as soon as its block is complete.
    private func loadSubtitles(_ url: String) async {
        subtitleLoading = true
        subtitleError = nil
        do {
            try await Self.readSubtitles(try await api.stream(at: url)) { [weak self] block in
                guard let self else { return }
                self.subtitles.add(block: block)
                self.showSubtitle(at: self.player.currentTime().seconds)
            }
            subtitleLoading = false
        } catch is CancellationError {
            subtitleLoading = false
        } catch {
            guard !Task.isCancelled else {
                subtitleLoading = false
                return
            }
            subtitleLoading = false
            subtitleError = error.localizedDescription
        }
    }

    /// The blocks of a WebVTT stream, parsed off the main actor (a long track is hundreds of
    /// thousands of bytes; the video should not wait for them). Each complete block is handed back.
    private nonisolated static func readSubtitles(_ bytes: URLSession.AsyncBytes,
                                                  onBlock: @escaping @MainActor @Sendable (String) -> Void) async throws {
        var line: [UInt8] = []
        var block: [String] = []
        for try await byte in bytes {
            if byte == 0x0D { continue }
            guard byte == 0x0A else {
                line.append(byte)
                continue
            }
            if line.isEmpty {
                if !block.isEmpty { await onBlock(block.joined(separator: "\n")) }
                block.removeAll()
            } else {
                block.append(String(decoding: line, as: UTF8.self))
                line.removeAll(keepingCapacity: true)
            }
        }
        if !line.isEmpty { block.append(String(decoding: line, as: UTF8.self)) }
        if !block.isEmpty { await onBlock(block.joined(separator: "\n")) }
    }

    private func showSubtitle(at t: Double) {
        let text = t.isFinite ? subtitles.text(at: t) : nil
        if text != subtitleText { subtitleText = text }
    }

    // MARK: ending

    private func fail(_ error: any Error) {
        finish(.failed(error.localizedDescription))
    }

    private func finish(_ end: PlaybackEnd) {
        guard !closed else { return }
        if !startReported {
            switch end {
            case let .failed(message): reportStart(message)
            case .stoppedElsewhere: reportStart("Playback was stopped.")
            case .finished, .closed: reportStart("The play ended before it started.")
            }
        }
        onEnd?(end)
    }

    private func reportStart(_ failure: String?) {
        guard !startReported else { return }
        startReported = true
        onStart?(failure)
    }

    // MARK: now playing & remote commands

    private func configureExternalMetadata(for playerItem: AVPlayerItem, detail: ItemDetail) {
        let item = detail.item
        var metadata: [AVMetadataItem] = []

        let titleItem = AVMutableMetadataItem()
        titleItem.identifier = .commonIdentifierTitle
        titleItem.value = (item.isEpisode ? (item.episodeTitle ?? item.title) : item.title) as (NSCopying & NSObjectProtocol)
        metadata.append(titleItem)

        let artistItem = AVMutableMetadataItem()
        artistItem.identifier = .commonIdentifierArtist
        artistItem.value = (item.isEpisode ? (item.show ?? "") : item.year.map(String.init) ?? "") as (NSCopying & NSObjectProtocol)
        metadata.append(artistItem)

        playerItem.externalMetadata = metadata
    }

    private func updateNowPlayingInfo() {
        guard let detail = item else { return }
        let item = detail.item
        var info = [String: Any]()
        info[MPMediaItemPropertyTitle] = item.isEpisode ? (item.episodeTitle ?? item.title) : item.title
        if item.isEpisode {
            info[MPMediaItemPropertyArtist] = item.show ?? ""
            if let code = item.episodeCode {
                info[MPMediaItemPropertyAlbumTitle] = code
            }
        } else if let year = item.year {
            info[MPMediaItemPropertyArtist] = String(year)
        }
        let dur = item.durationSec > 0 ? item.durationSec : (player.currentItem?.duration.seconds ?? 0)
        if dur > 0 && dur.isFinite {
            info[MPMediaItemPropertyPlaybackDuration] = dur
        }
        let pos = player.currentTime().seconds
        if pos.isFinite {
            info[MPNowPlayingInfoPropertyElapsedPlaybackTime] = pos
        }
        info[MPNowPlayingInfoPropertyPlaybackRate] = player.rate
        if let art = nowPlayingArtwork {
            info[MPMediaItemPropertyArtwork] = art
        }
        MPNowPlayingInfoCenter.default().nowPlayingInfo = info
    }

    private func loadArtwork(for detail: ItemDetail) {
        let item = detail.item
        guard let artPath = item.landscapeImage ?? item.poster,
              let url = api.url(artPath) else { return }
        Task { [weak self] in
            guard let (data, _) = try? await URLSession.shared.data(from: url),
                  let image = UIImage(data: data) else { return }
            await MainActor.run { [weak self] in
                guard let self, !self.closed else { return }
                self.nowPlayingArtwork = Self.artwork(for: image)
                self.updateNowPlayingInfo()
            }
        }
    }

    /// The lock-screen artwork. MediaRemote asks its handler for an image on its own queue, so the
    /// handler must not inherit the main actor's isolation (that trips a main-queue assertion).
    nonisolated static func artwork(for image: UIImage) -> MPMediaItemArtwork {
        MPMediaItemArtwork(boundsSize: image.size) { _ in image }
    }

    private func clearRemoteCommands() {
        for entry in remoteCommandTargets {
            entry.command.removeTarget(entry.target)
        }
        remoteCommandTargets.removeAll()
    }

    private func setupRemoteCommands() {
        clearRemoteCommands()
        let center = MPRemoteCommandCenter.shared()

        let playTarget = center.playCommand.addTarget { [weak self] _ in
            self?.resume()
            return .success
        }
        remoteCommandTargets.append((center.playCommand, playTarget))

        let pauseTarget = center.pauseCommand.addTarget { [weak self] _ in
            self?.pause()
            return .success
        }
        remoteCommandTargets.append((center.pauseCommand, pauseTarget))

        let toggleTarget = center.togglePlayPauseCommand.addTarget { [weak self] _ in
            guard let self else { return .commandFailed }
            if self.player.timeControlStatus == .playing {
                self.pause()
            } else {
                self.resume()
            }
            return .success
        }
        remoteCommandTargets.append((center.togglePlayPauseCommand, toggleTarget))

        let seekTarget = center.changePlaybackPositionCommand.addTarget { [weak self] event in
            guard let self, let posEvent = event as? MPChangePlaybackPositionCommandEvent else { return .commandFailed }
            self.seek(to: posEvent.positionTime)
            return .success
        }
        remoteCommandTargets.append((center.changePlaybackPositionCommand, seekTarget))

        center.skipForwardCommand.preferredIntervals = [15]
        let fwdTarget = center.skipForwardCommand.addTarget { [weak self] _ in
            guard let self else { return .commandFailed }
            self.seek(to: self.position + 15)
            return .success
        }
        remoteCommandTargets.append((center.skipForwardCommand, fwdTarget))

        center.skipBackwardCommand.preferredIntervals = [15]
        let backTarget = center.skipBackwardCommand.addTarget { [weak self] _ in
            guard let self else { return .commandFailed }
            self.seek(to: max(0, self.position - 15))
            return .success
        }
        remoteCommandTargets.append((center.skipBackwardCommand, backTarget))
    }
}
