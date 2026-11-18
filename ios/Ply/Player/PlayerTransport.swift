#if os(iOS)
import AVFoundation
import Observation
import SwiftUI

/// What a remote screen reads and drives: a play on a TV. `PlayerTransport` is AirPlay's; a Chromecast
/// session can be another, and drive the same `PlayerRemoteView` (docs/M11-CLIENTS.md, "The remote").
protocol RemoteTransport: AnyObject, Observable {
    var isPlaying: Bool { get }
    /// Where the play is, or where the person is dragging the timeline to.
    var shownPosition: Double { get }
    var duration: Double { get }
    var speed: Float { get }
    /// The TV's name, while the play is on one.
    var routeName: String? { get }
    func togglePlay()
    func skip(by seconds: Double)
    func scrub(to seconds: Double?)
    func seek(to seconds: Double)
    func setSpeed(_ speed: Float)
}

/// The state of a play as its controls show it, and what they ask of it: position, length, playing or
/// not, speed, mute, and whether (and where) it is on a TV.
@Observable final class PlayerTransport: RemoteTransport {
    static let skipInterval: Double = 10
    static let speeds: [Float] = [0.5, 0.75, 1, 1.25, 1.5, 2]

    let playback: PlaybackController
    private(set) var isPlaying = false
    private(set) var position: Double = 0
    private(set) var speed: Float = 1
    private(set) var isMuted = false
    private(set) var isExternal = false
    private(set) var routeName: String?
    /// Where the person is dragging the timeline to, shown in place of the player's position.
    private(set) var scrubPosition: Double?
    private var loadedDuration: Double = 0

    @ObservationIgnored private var timeObserver: Any?
    @ObservationIgnored private var observations: [NSKeyValueObservation] = []
    @ObservationIgnored private var routeChanges: Task<Void, Never>?

    init(playback: PlaybackController) {
        self.playback = playback
        let player = playback.player
        speed = player.defaultRate
        timeObserver = player.addPeriodicTimeObserver(forInterval: CMTime(value: 1, timescale: 4), queue: .main) { [weak self] time in
            MainActor.assumeIsolated { self?.tick(time.seconds) }
        }
        observations = [
            player.observe(\.timeControlStatus, options: [.initial, .new]) { [weak self] p, _ in
                let playing = p.timeControlStatus != .paused
                Task { @MainActor in self?.isPlaying = playing }
            },
            player.observe(\.isExternalPlaybackActive, options: [.initial, .new]) { [weak self] p, _ in
                let external = p.isExternalPlaybackActive
                Task { @MainActor in
                    self?.isExternal = external
                    self?.readRoute()
                }
            },
            player.observe(\.isMuted, options: [.initial, .new]) { [weak self] p, _ in
                let muted = p.isMuted
                Task { @MainActor in self?.isMuted = muted }
            },
        ]
        routeChanges = Task { [weak self] in
            for await _ in NotificationCenter.default.notifications(named: AVAudioSession.routeChangeNotification) {
                guard let self else { return }
                self.readRoute()
            }
        }
    }

    /// Lets go of the player: called when the play's screen is taken down.
    func detach() {
        if let timeObserver { playback.player.removeTimeObserver(timeObserver) }
        timeObserver = nil
        observations = []
        routeChanges?.cancel()
        routeChanges = nil
    }

    /// The play's length: the library's, else the stream's once it is known.
    var duration: Double {
        let known = playback.itemDetail?.item.durationSec ?? 0
        return known > 0 ? known : loadedDuration
    }

    var shownPosition: Double { scrubPosition ?? position }

    /// A film's name, or an episode's code and name, for the controls' title.
    var title: String {
        guard let item = playback.itemDetail?.item else { return "" }
        guard item.isEpisode else { return item.title }
        return Format.facts(item.episodeCode, item.episodeTitle ?? item.title)
    }

    func togglePlay() {
        if isPlaying { playback.pause() } else { playback.resume() }
    }

    func skip(by seconds: Double) {
        seek(to: position + seconds)
    }

    /// The person dragging the timeline (`nil` when they let go without a seek).
    func scrub(to seconds: Double?) {
        scrubPosition = seconds.map(clamped)
    }

    func seek(to seconds: Double) {
        let target = clamped(seconds)
        scrubPosition = nil
        position = target
        playback.seek(to: target)
    }

    func setSpeed(_ speed: Float) {
        self.speed = speed
        playback.setSpeed(speed)
    }

    func toggleMute() {
        playback.player.isMuted.toggle()
    }

    /// "1×", "1.5×", "0.75×".
    static func speedLabel(_ speed: Float) -> String {
        "\(speed.formatted(.number.precision(.fractionLength(0...2))))×"
    }

    private func clamped(_ seconds: Double) -> Double {
        let upper = duration > 0 ? duration : .greatestFiniteMagnitude
        return min(max(0, seconds), upper)
    }

    private func tick(_ seconds: Double) {
        if seconds.isFinite { position = seconds }
        let length = playback.player.currentItem?.duration.seconds ?? 0
        if length.isFinite, length > 0, length != loadedDuration { loadedDuration = length }
    }

    private func readRoute() {
        let outputs = AVAudioSession.sharedInstance().currentRoute.outputs
        routeName = isExternal ? (outputs.first { $0.portType == .airPlay } ?? outputs.first)?.portName : nil
    }
}

/// Whether the player's controls are showing: a tap shows or hides them, and they hide together after a
/// few seconds of playing. A pause, or a finger on the timeline, keeps them up; so does VoiceOver or Switch
/// Control, since hidden controls leave those users nothing to reach.
@Observable final class PlayerChrome {
    /// Whether VoiceOver or Switch Control is on.
    static let assistiveTechnologyRunning: () -> Bool = {
        UIAccessibility.isVoiceOverRunning || UIAccessibility.isSwitchControlRunning
    }

    private(set) var visible = true
    /// Something the person is doing with the controls (a scrub) that must not be hidden under them.
    var holding = false {
        didSet { holding ? hideTask?.cancel() : scheduleHide() }
    }
    @ObservationIgnored private let hideDelay: Duration
    @ObservationIgnored private let assistiveTechnologyRunning: () -> Bool
    @ObservationIgnored private var hideTask: Task<Void, Never>?
    @ObservationIgnored private var statusObservation: NSKeyValueObservation?
    @ObservationIgnored private var assistiveChanges: Task<Void, Never>?
    @ObservationIgnored private weak var player: AVPlayer?

    init(player: AVPlayer, hideDelay: Duration = .seconds(3.5),
         assistiveTechnologyRunning: @escaping () -> Bool = PlayerChrome.assistiveTechnologyRunning) {
        self.player = player
        self.hideDelay = hideDelay
        self.assistiveTechnologyRunning = assistiveTechnologyRunning
        statusObservation = player.observe(\.timeControlStatus, options: [.initial, .new]) { [weak self] p, _ in
            let paused = p.timeControlStatus == .paused
            Task { @MainActor in
                guard let self else { return }
                if paused {
                    self.hideTask?.cancel()
                    self.show(true)
                } else {
                    self.scheduleHide()
                }
            }
        }
        // VoiceOver or Switch Control turned on while the controls are hidden: bring them back.
        assistiveChanges = Task { [weak self] in
            let center = NotificationCenter.default
            let changes = center.notifications(named: UIAccessibility.voiceOverStatusDidChangeNotification)
            for await _ in changes {
                guard let self else { return }
                if self.assistiveTechnologyRunning() { self.wake() }
            }
        }
    }

    /// A tap on the picture.
    func toggle() {
        if visible, !assistiveTechnologyRunning() { hideTask?.cancel(); show(false) } else { wake() }
    }

    /// The person touched a control: keep them up a while longer.
    func wake() {
        show(true)
        scheduleHide()
    }

    func detach() {
        hideTask?.cancel()
        assistiveChanges?.cancel()
        statusObservation = nil
    }

    private func show(_ shown: Bool) {
        guard visible != shown else { return }
        withAnimation(.easeInOut(duration: 0.25)) { visible = shown }
    }

    /// Whether the controls may hide on their own now.
    private var mayHide: Bool {
        !holding && player?.timeControlStatus != .paused && !assistiveTechnologyRunning()
    }

    private func scheduleHide() {
        hideTask?.cancel()
        guard mayHide else { return }
        hideTask = Task { [weak self, hideDelay] in
            try? await Task.sleep(for: hideDelay)
            guard !Task.isCancelled, let self, self.mayHide else { return }
            self.show(false)
        }
    }
}
#endif
