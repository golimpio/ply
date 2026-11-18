#if os(iOS)
import AVKit
import MediaPlayer
import SwiftUI

/// Frames of the player's controls, for the layout tests.
nonisolated struct PlayerLayoutFramesKey: PreferenceKey {
    static let defaultValue: [String: CGRect] = [:]
    static func reduce(value: inout [String: CGRect], nextValue: () -> [String: CGRect]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}

extension View {
    func playerFrame(_ id: String) -> some View {
        background(GeometryReader { geo in
            Color.clear.preference(key: PlayerLayoutFramesKey.self, value: [id: geo.frame(in: .global)])
        })
    }
}

/// Picture in Picture for the player's layer, and whether it can start now (the button is off until it can).
@Observable final class PlayerPictureInPicture {
    private(set) var isPossible = false
    private(set) var isActive = false
    @ObservationIgnored private(set) var controller: AVPictureInPictureController?
    @ObservationIgnored private var observations: [NSKeyValueObservation] = []

    func attach(_ layer: AVPlayerLayer, delegate: AVPictureInPictureControllerDelegate) {
        guard AVPictureInPictureController.isPictureInPictureSupported(),
              let controller = AVPictureInPictureController(playerLayer: layer) else { return }
        controller.delegate = delegate
        // The app going to the background while it plays takes it into PiP, as AVKit's player did.
        controller.canStartPictureInPictureAutomaticallyFromInline = true
        self.controller = controller
        observations = [
            controller.observe(\.isPictureInPicturePossible, options: [.initial, .new]) { [weak self] c, _ in
                let possible = c.isPictureInPicturePossible
                Task { @MainActor in self?.isPossible = possible }
            },
            controller.observe(\.isPictureInPictureActive, options: [.initial, .new]) { [weak self] c, _ in
                let active = c.isPictureInPictureActive
                Task { @MainActor in self?.isActive = active }
            },
        ]
    }

    func toggle() {
        guard let controller else { return }
        if controller.isPictureInPictureActive {
            controller.stopPictureInPicture()
        } else {
            controller.startPictureInPicture()
        }
    }

    func detach() {
        observations = []
        controller?.delegate = nil
        controller = nil
    }
}

/// ply's own controls over the picture (docs/M11-CLIENTS.md, "The player"): close, Picture in Picture and
/// AirPlay on top; back, play/pause and forward in the middle; the title, speed, tracks and mute over the
/// timeline at the foot. While the play is on a TV, the remote takes the picture's place.
struct PlayerControls: View {
    let transport: PlayerTransport
    let chrome: PlayerChrome
    let pictureInPicture: PlayerPictureInPicture
    var onClose: () -> Void
    /// A pinch: true fills the screen, false fits the whole picture.
    var onFill: (Bool) -> Void

    var body: some View {
        if transport.isExternal {
            PlayerRemoteView(transport: transport, playback: transport.playback, onStop: onClose)
        } else {
            ZStack {
                gestureSurface
                SubtitleOverlay(playback: transport.playback,
                                lift: chrome.visible ? PlyDesign.playerSubtitleLiftWithControls : PlyDesign.playerSubtitleLift)
                    .animation(.easeInOut(duration: 0.25), value: chrome.visible)
                controls
                    .opacity(chrome.visible ? 1 : 0)
                    .allowsHitTesting(chrome.visible)
                    .accessibilityHidden(!chrome.visible)
            }
        }
    }

    /// The picture itself: a tap shows or hides the controls, a pinch fills or fits, a swipe down closes.
    private var gestureSurface: some View {
        Color.clear
            .contentShape(Rectangle())
            .onTapGesture { chrome.toggle() }
            .gesture(MagnifyGesture().onEnded { onFill($0.magnification > 1) })
            .simultaneousGesture(DragGesture(minimumDistance: 24).onEnded { drag in
                let down = drag.translation.height
                if down > PlyDesign.playerSwipeToClose, abs(drag.translation.width) < down { onClose() }
            })
            .accessibilityHidden(true)
    }

    private var controls: some View {
        VStack(spacing: 0) {
            HStack(spacing: PlyDesign.glassCircleGroupSpacing) {
                GlassCloseButton(action: onClose)
                    .playerFrame("close")
                Spacer(minLength: 0)
                Button { pictureInPicture.toggle() } label: {
                    GlassCircleIconLabel(systemImage: pictureInPicture.isActive ? "pip.exit" : "pip.enter")
                }
                .buttonStyle(.plain)
                .disabled(!pictureInPicture.isPossible)
                .accessibilityLabel("Picture in Picture")
                .playerFrame("pip")
                PlayerRouteButton(active: transport.isExternal)
                    .playerFrame("airplay")
            }
            Spacer(minLength: 0)
            PlayerTransportButtons(transport: transport)
            Spacer(minLength: 0)
            VStack(spacing: PlyDesign.sectionHeaderSpacing) {
                HStack(spacing: PlyDesign.glassCircleGroupSpacing) {
                    Text(transport.title)
                        .font(.headline)
                        .foregroundStyle(.white)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .shadow(color: .black.opacity(0.4), radius: 3, y: 1)
                        .playerFrame("title")
                    PlayerSpeedMenu(transport: transport)
                        .playerFrame("speed")
                    PlayerTrackMenu(playback: transport.playback)
                        .playerFrame("tracks")
                    Button { transport.toggleMute() } label: {
                        GlassCircleIconLabel(systemImage: transport.isMuted ? "speaker.slash.fill" : "speaker.wave.2.fill")
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(transport.isMuted ? "Unmute" : "Mute")
                    .playerFrame("mute")
                }
                PlayerTimeline(transport: transport, onScrubbing: { chrome.holding = $0 })
                    .playerFrame("timeline")
            }
        }
        .padding(.horizontal, PlyDesign.contentHorizontalPadding)
        .padding(.vertical, PlyDesign.overlayTopOffset)
        .background { scrims }
        .simultaneousGesture(TapGesture().onEnded { chrome.wake() })
    }

    /// Dark gradients at the top and the foot, so the white controls read over a bright picture.
    private var scrims: some View {
        VStack(spacing: 0) {
            LinearGradient(colors: [.black.opacity(PlyDesign.playerScrimOpacity), .clear], startPoint: .top, endPoint: .bottom)
                .frame(height: PlyDesign.playerScrimHeight)
            Spacer(minLength: 0)
            LinearGradient(colors: [.clear, .black.opacity(PlyDesign.playerScrimOpacity)], startPoint: .top, endPoint: .bottom)
                .frame(height: PlyDesign.playerScrimHeight)
        }
        .ignoresSafeArea()
        .allowsHitTesting(false)
    }
}

/// Back 10 s, play/pause, forward 10 s: the player's middle and the remote's.
struct PlayerTransportButtons: View {
    let transport: any RemoteTransport

    var body: some View {
        HStack(spacing: PlyDesign.playerTransportSpacing) {
            Button { transport.skip(by: -PlayerTransport.skipInterval) } label: {
                GlassCircleIconLabel(systemImage: "gobackward.10", size: PlyDesign.playerSkipSize)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Back 10 seconds")
            .playerFrame("back")
            Button { transport.togglePlay() } label: {
                GlassCircleIconLabel(systemImage: transport.isPlaying ? "pause.fill" : "play.fill",
                                     size: PlyDesign.heroPlaySize, glyphSize: PlyDesign.heroPlayGlyphSize)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(transport.isPlaying ? "Pause" : "Play")
            .playerFrame("playPause")
            Button { transport.skip(by: PlayerTransport.skipInterval) } label: {
                GlassCircleIconLabel(systemImage: "goforward.10", size: PlyDesign.playerSkipSize)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Forward 10 seconds")
            .playerFrame("forward")
        }
    }
}

/// The timeline: a glass capsule with the time played, the bar, and the time left. A finger anywhere on
/// the bar drags the position, and the play seeks there when it lets go. A drag the system cancels (an edge
/// swipe, a call) lets go without a seek. Until the play's length is known, the bar does nothing.
struct PlayerTimeline: View {
    let transport: any RemoteTransport
    var onScrubbing: (Bool) -> Void = { _ in }
    /// True while a finger is on the bar; SwiftUI resets it however the drag ends, cancelled included.
    @GestureState private var dragging = false

    var body: some View {
        let duration = transport.duration
        let position = transport.shownPosition
        let fraction = duration > 0 ? min(1, max(0, position / duration)) : 0
        HStack(spacing: PlyDesign.capsuleHorizontalPadding) {
            Text(Format.clock(position))
                .playerFrame("elapsed")
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(.white.opacity(0.25))
                    Capsule().fill(Color.plyAccent).frame(width: geo.size.width * fraction)
                }
                .frame(height: 4)
                .frame(maxHeight: .infinity)
                .contentShape(Rectangle())
                .gesture(DragGesture(minimumDistance: 0)
                    .updating($dragging) { _, state, _ in state = true }
                    .onChanged { drag in
                        guard duration > 0 else { return }
                        onScrubbing(true)
                        transport.scrub(to: seconds(at: drag.location.x, width: geo.size.width, duration: duration))
                    }
                    .onEnded { drag in
                        guard duration > 0 else { return }
                        transport.seek(to: seconds(at: drag.location.x, width: geo.size.width, duration: duration))
                    })
            }
            .onChange(of: dragging) { _, now in
                guard !now else { return }
                // After a seek this finds nothing left to clear; after a cancel it is what lets go.
                transport.scrub(to: nil)
                onScrubbing(false)
            }
            Text("-\(Format.clock(max(0, duration - position)))")
                .playerFrame("remaining")
        }
        .font(.caption.weight(.semibold))
        .monospacedDigit()
        .foregroundStyle(.white)
        .padding(.horizontal, PlyDesign.capsuleHorizontalPadding + PlyDesign.capsuleVerticalPadding)
        .frame(height: PlyDesign.iconButtonSize)
        .plyGlassCapsule(tintOpacity: 0.16)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Timeline")
        .accessibilityValue("\(Format.clock(position)) of \(Format.clock(duration))")
        .accessibilityAdjustableAction { direction in
            switch direction {
            case .increment: transport.skip(by: PlayerTransport.skipInterval)
            case .decrement: transport.skip(by: -PlayerTransport.skipInterval)
            @unknown default: break
            }
        }
    }

    private func seconds(at x: CGFloat, width: CGFloat, duration: Double) -> Double {
        guard width > 0 else { return 0 }
        return Double(min(1, max(0, x / width))) * duration
    }
}

/// The speed, shown as "1×" in a glass circle; the menu picks 0.5× to 2×.
struct PlayerSpeedMenu: View {
    let transport: any RemoteTransport

    var body: some View {
        Menu {
            Picker("Speed", selection: Binding(get: { transport.speed }, set: { transport.setSpeed($0) })) {
                ForEach(PlayerTransport.speeds, id: \.self) { speed in
                    Text(PlayerTransport.speedLabel(speed)).tag(speed)
                }
            }
        } label: {
            GlassCircle(selected: transport.speed != 1) {
                Text(PlayerTransport.speedLabel(transport.speed))
                    .font(.system(size: PlyDesign.iconButtonGlyphSize - 4, weight: .bold))
                    .foregroundStyle(transport.speed != 1 ? .black : .white)
                    .minimumScaleFactor(0.7)
            }
        }
        .accessibilityLabel("Speed")
        .accessibilityValue(PlayerTransport.speedLabel(transport.speed))
    }
}

/// Audio and subtitles in one menu, two sections; the bubble fills in the accent while subtitles are on.
struct PlayerTrackMenu: View {
    let playback: PlaybackController

    var body: some View {
        let subtitlesOn = playback.selectedSubtitle != nil
        Menu {
            Section("Audio") {
                if playback.availableAudioStreams.isEmpty {
                    Text("Default Audio")
                }
                ForEach(playback.availableAudioStreams, id: \.index) { stream in
                    check(stream.label, on: playback.activeAudioIndex == stream.index) {
                        await playback.selectAudio(stream.index)
                    }
                }
            }
            Section("Subtitles") {
                check("Off", on: !subtitlesOn) { await playback.selectSubtitle(nil) }
                ForEach(playback.availableSubtitles, id: \.0.key) { sub, choice in
                    check(sub.image ? "\(sub.label) (burned in)" : sub.label, on: playback.selectedSubtitle == choice) {
                        await playback.selectSubtitle(choice)
                    }
                }
            }
        } label: {
            GlassCircleIconLabel(systemImage: subtitlesOn ? "captions.bubble.fill" : "captions.bubble",
                                 color: subtitlesOn ? .plyAccent : .white)
        }
        .accessibilityLabel("Audio and Subtitles")
    }

    private func check(_ title: String, on: Bool, _ action: @escaping () async -> Void) -> some View {
        Button {
            Task { await action() }
        } label: {
            if on { Label(title, systemImage: "checkmark") } else { Text(title) }
        }
    }
}

/// AirPlay: the system's route picker, under the same glass circle as the other controls (its own glyph is
/// hidden, so the circle's shows). Filled in the accent while the play is on a TV.
struct PlayerRouteButton: View {
    var active: Bool

    var body: some View {
        GlassCircleIconLabel(systemImage: "airplayvideo", selected: active)
            .accessibilityHidden(true)
            .overlay { RoutePicker() }
    }

    private struct RoutePicker: UIViewRepresentable {
        func makeUIView(context: Context) -> AVRoutePickerView {
            let picker = AVRoutePickerView()
            picker.prioritizesVideoDevices = true
            picker.tintColor = .clear
            picker.activeTintColor = .clear
            picker.accessibilityLabel = String(localized: "AirPlay")
            return picker
        }

        func updateUIView(_ uiView: AVRoutePickerView, context: Context) {}
    }
}

/// The TV's volume (the system's own slider, which drives an AirPlay route's volume).
struct PlayerVolumeSlider: UIViewRepresentable {
    func makeUIView(context: Context) -> MPVolumeView {
        let view = MPVolumeView()
        view.tintColor = UIColor(Color.plyAccent)
        return view
    }

    func updateUIView(_ uiView: MPVolumeView, context: Context) {}
}

/// The remote, in place of the picture while the play is on a TV (docs/M11-CLIENTS.md, "The remote"): the
/// poster over its own colours, where it plays, the title and facts, the timeline and transport, then
/// speed, tracks, the TV's volume and Stop.
struct PlayerRemoteView: View {
    let transport: any RemoteTransport
    let playback: PlaybackController
    var onStop: () -> Void
    @State private var poster: UIImage?
    @State private var palette: PosterPalette = .fallback

    var body: some View {
        let item = playback.itemDetail?.item
        ZStack {
            LivingMeshBackdrop(palette: palette)
                .ignoresSafeArea()
            VStack(spacing: PlyDesign.sectionSpacing) {
                HStack {
                    Label {
                        if let name = transport.routeName { Text("Playing on \(name)") } else { Text("Playing on TV") }
                    } icon: {
                        Image(systemName: "tv")
                    }
                        .plyMicroSpecsStyle()
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .playerFrame("remoteRoute")
                    PlayerRouteButton(active: true)
                }
                Spacer(minLength: 0)
                Group {
                    if let poster {
                        Image(uiImage: poster).resizable().scaledToFit()
                    } else {
                        Rectangle().fill(.quaternary).aspectRatio(2.0 / 3.0, contentMode: .fit)
                    }
                }
                .clipShape(.rect(cornerRadius: PlyDesign.panelCornerRadius))
                .shadow(color: .black.opacity(0.4), radius: 20, y: 10)
                .frame(maxHeight: .infinity)
                .accessibilityHidden(true)
                .playerFrame("remotePoster")
                VStack(spacing: PlyDesign.resultRowSpacing) {
                    Text(item.map { $0.isEpisode ? Format.facts($0.show, $0.episodeCode) : $0.title } ?? "")
                        .plyHeroTitleStyle(size: 24)
                        .multilineTextAlignment(.center)
                    if let item {
                        Text(Format.facts(item.isEpisode ? (item.episodeTitle ?? item.title) : item.year.map(String.init),
                                          item.durationSec > 0 ? Format.runtime(item.durationSec) : nil))
                            .plyMicroSpecsStyle()
                            .textCase(.uppercase)
                    }
                }
                .playerFrame("remoteTitle")
                PlayerTimeline(transport: transport)
                    .playerFrame("remoteTimeline")
                PlayerTransportButtons(transport: transport)
                HStack(spacing: PlyDesign.glassCircleGroupSpacing) {
                    PlayerSpeedMenu(transport: transport)
                    PlayerTrackMenu(playback: playback)
                    PlayerVolumeSlider()
                        .frame(height: PlyDesign.iconButtonSize)
                        .padding(.horizontal, PlyDesign.capsuleHorizontalPadding)
                    GlassIconButton(systemImage: "stop.fill", accessibilityLabel: "Stop", action: onStop)
                        .playerFrame("remoteStop")
                }
            }
            .padding(.horizontal, PlyDesign.contentHorizontalPadding)
            .padding(.vertical, PlyDesign.overlayTopOffset)
        }
        .task(id: item?.poster) {
            guard let url = playback.imageURL(item?.poster) else { return }
            async let image = ImagePipeline.shared.image(for: url)
            async let colours = PosterPaletteExtractor.shared.palette(for: url)
            let (loaded, extracted) = await (image, colours)
            guard !Task.isCancelled else { return }
            poster = loaded
            palette = extracted
        }
    }
}
#endif
