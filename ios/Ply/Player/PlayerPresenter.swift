import AVKit
import SwiftUI
import UIKit

/// Shows a play full screen in AVKit's own player, modally from the top of the app, so that it has
/// AVKit's close button, AirPlay route picker and gestures. Text subtitles are drawn on its content
/// overlay.
final class PlayerPresenter: NSObject, AVPlayerViewControllerDelegate, UIAdaptivePresentationControllerDelegate {
    private var current: AVPlayerViewController?
    private var overlay: UIViewController?
    private var trackHost: UIViewController?
    private var onDismiss: (() -> Void)?
    /// The window this presenter belongs to: with two iPad windows open, the play goes in this one, not
    /// in whichever UIKit calls active first.
    private weak var hostScene: UIWindowScene?
    /// Picture in Picture: the player is off the screen on purpose, so its leaving the window is
    /// not the person closing it. A restore brings the player back; closing the PiP window ends it.
    private var pipActive = false
    private var pipRestoring = false

    /// Shows the play, in place of the one showing; onDismiss is called when the person closes it.
    func present(_ playback: PlaybackController, in windowScene: UIWindowScene?, onDismiss: @escaping () -> Void) {
        hostScene = windowScene
        let vc = AVPlayerViewController()
        vc.player = playback.player
        vc.delegate = self
        vc.modalPresentationStyle = .fullScreen
        #if os(iOS)
        vc.allowsPictureInPicturePlayback = true
        vc.canStartPictureInPictureAutomaticallyFromInline = true
        vc.updatesNowPlayingInfoCenter = false
        #elseif os(tvOS)
        vc.transportBarCustomMenuItems = Self.tvTransportMenus(for: playback)
        #endif
        pipActive = false
        pipRestoring = false
        vc.loadViewIfNeeded()
        if let content = vc.contentOverlayView {
            let host = UIHostingController(rootView: SubtitleOverlay(playback: playback))
            host.view.backgroundColor = .clear
            host.view.isUserInteractionEnabled = false
            host.view.translatesAutoresizingMaskIntoConstraints = false
            vc.addChild(host)
            content.addSubview(host.view)
            NSLayoutConstraint.activate([
                host.view.leadingAnchor.constraint(equalTo: content.leadingAnchor),
                host.view.trailingAnchor.constraint(equalTo: content.trailingAnchor),
                host.view.topAnchor.constraint(equalTo: content.topAnchor),
                host.view.bottomAnchor.constraint(equalTo: content.bottomAnchor),
            ])
            overlay = host
            host.didMove(toParent: vc)
        }
        // Should AVKit not tell its delegate of a close, the player leaving the screen says it:
        // a play nobody can see must not be kept alive by its heartbeats. Picture in Picture is
        // the exception: the play is meant to be off the screen. It sits on the player's own view,
        // not on the content overlay, because tvOS does not always vend one.
        vc.view.addSubview(WindowWatcher { [weak self, weak vc] in
            guard let self, let vc, !self.pipActive, !self.pipRestoring,
                  vc.presentingViewController == nil || vc.isBeingDismissed else { return }
            self.closed(vc)
        })

        #if os(iOS)
        installTrackBar(playback: playback, on: vc)
        #endif

        let old = current
        current = vc
        self.onDismiss = onDismiss
        let show: () -> Void = { [weak self] in
            self?.afterTransitions { [weak self] in
                guard let self, self.current === vc else { return }
                guard let top = self.top() else {
                    // There is no window to show it in: the play ends rather than running unseen.
                    let onDismiss = self.onDismiss
                    self.current = nil
                    self.onDismiss = nil
                    onDismiss?()
                    return
                }
                #if os(iOS)
                top.view.window?.endEditing(true)
                #endif
                top.present(vc, animated: true) { [weak self] in
                    vc.presentationController?.delegate = self
                }
            }
        }
        if let old, old.presentingViewController != nil {
            afterTransitions { old.dismiss(animated: false, completion: show) }
        } else {
            show()
        }
    }

    /// Closes the player; completion runs once it is gone (an alert can show then).
    func dismiss(completion: @escaping () -> Void = {}) {
        guard let vc = current else {
            completion()
            return
        }
        current = nil
        overlay = nil
        trackHost = nil
        onDismiss = nil
        pipActive = false
        pipRestoring = false
        // A play that fails while the player is still coming up is dismissed once it is up.
        afterTransitions {
            if vc.presentingViewController != nil {
                vc.dismiss(animated: true, completion: completion)
            } else {
                completion()
            }
        }
    }

    #if os(iOS)
    /// The close button, or a swipe down: called for a modal player as well as a full-screen one.
    func playerViewController(_ vc: AVPlayerViewController,
                              willEndFullScreenPresentationWithAnimationCoordinator coordinator: any UIViewControllerTransitionCoordinator) {
        guard !pipActive, !pipRestoring else { return }
        coordinator.animate(alongsideTransition: nil) { [weak self] context in
            guard !context.isCancelled else { return }
            self?.closed(vc)
        }
    }

    /// The player leaves the screen for the PiP window: a play that is not closed.
    func playerViewControllerWillStartPictureInPicture(_ playerViewController: AVPlayerViewController) {
        guard current === playerViewController else { return }
        pipActive = true
    }

    /// The person tapped the button that brings the player back: present it again and keep playing.
    func playerViewController(_ playerViewController: AVPlayerViewController,
                              restoreUserInterfaceForPictureInPictureStopWithCompletionHandler completionHandler: @escaping (Bool) -> Void) {
        guard current === playerViewController else {
            completionHandler(false)
            return
        }
        pipRestoring = true
        if playerViewController.presentingViewController == nil, let top = top(), !playerViewController.isBeingPresented {
            top.present(playerViewController, animated: true) { [weak self, weak playerViewController] in
                playerViewController?.presentationController?.delegate = self
                completionHandler(true)
            }
        } else if playerViewController.presentingViewController != nil {
            completionHandler(true)
        } else {
            // No window to restore it into: let the system stop the PiP stream.
            pipRestoring = false
            completionHandler(false)
        }
    }

    /// The PiP window is gone. If the person asked to come back, the player is up again; otherwise
    /// they closed the window, which ends the play.
    func playerViewControllerDidStopPictureInPicture(_ playerViewController: AVPlayerViewController) {
        guard current === playerViewController else {
            pipActive = false
            pipRestoring = false
            return
        }
        pipActive = false
        if pipRestoring {
            pipRestoring = false
            return
        }
        closed(playerViewController)
    }
#endif

    /// The person closed the player showing.
    private func closed(_ vc: AVPlayerViewController) {
        guard current === vc else { return }
        let done = onDismiss
        current = nil
        overlay = nil
        trackHost = nil
        onDismiss = nil
        done?()
    }

    func presentationControllerDidDismiss(_ presentationController: UIPresentationController) {
        if let vc = presentationController.presentedViewController as? AVPlayerViewController {
            closed(vc)
        }
    }

    /// The root of this presenter's window. A window that is not the key one still has a root, and a
    /// presenter that was never told which window it belongs to will not guess between two.
    private func root() -> UIViewController? {
        guard let scene = hostScene ?? Self.soleScene() else { return nil }
        return (scene.keyWindow ?? scene.windows.first)?.rootViewController
    }

    private func top() -> UIViewController? {
        var vc = root()
        while let next = vc?.presentedViewController, !next.isBeingDismissed {
            vc = next
        }
        return vc
    }

    /// The one window there is, when this presenter was not told which one; with two windows open it
    /// does not choose. (tvOS has one; a phone has one.)
    private static func soleScene() -> UIWindowScene? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        return scenes.count == 1 ? scenes.first : nil
    }

    /// Runs action once no presentation or dismissal is under way: UIKit drops one asked for during
    /// another (a play that fails as its player comes up, a play started as the last one closes). It
    /// reads this presenter's window, as the presentation itself does.
    private func afterTransitions(_ action: @escaping () -> Void) {
        var vc = root()
        while let v = vc {
            if let coordinator = v.transitionCoordinator {
                coordinator.animate(alongsideTransition: nil) { [weak self] _ in self?.afterTransitions(action) }
                return
            }
            vc = v.presentedViewController
        }
        action()
    }
}

/// Tells when the player it is in leaves the window, after having been in one.
private final class WindowWatcher: UIView {
    private let onLeave: () -> Void
    private var shown = false

    init(onLeave: @escaping () -> Void) {
        self.onLeave = onLeave
        super.init(frame: .zero)
        isUserInteractionEnabled = false
    }

    required init?(coder: NSCoder) { nil }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        if window != nil {
            shown = true
        } else if shown {
            // After UIKit has finished with the dismissal, so that it can be told from a move.
            Task { @MainActor [weak self, onLeave] in
                await Task.yield()
                guard self?.window == nil else { return }
                onLeave()
            }
        }
    }
}

/// The text subtitle of a play, at the foot of the picture. A track that is still being extracted
/// says so; one that failed says that instead of silently never appearing.
struct SubtitleOverlay: View {
    let playback: PlaybackController

    var body: some View {
        VStack {
            Spacer()
            if let text = playback.subtitleText {
                Text(text)
                    .font(.title3.weight(.semibold))
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.white)
                    .shadow(color: .black, radius: 2)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                    .background(.black.opacity(0.45), in: .rect(cornerRadius: 6))
                    .padding(.horizontal, 24)
                    .padding(.bottom, 56)
            } else if playback.subtitleLoading {
                HStack(spacing: 8) {
                    ProgressView().tint(.white)
                    Text("Downloading subtitles…")
                }
                .font(.caption)
                .foregroundStyle(.white)
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background(.black.opacity(0.45), in: .rect(cornerRadius: 6))
                .padding(.bottom, 56)
            } else if playback.subtitleError != nil {
                Text("Subtitles could not be loaded")
                    .font(.caption)
                    .foregroundStyle(.white)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(.black.opacity(0.45), in: .rect(cornerRadius: 6))
                    .padding(.bottom, 56)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

#if os(iOS)
extension PlayerPresenter {
    func installTrackBar(playback: PlaybackController, on vc: AVPlayerViewController) {
        let chrome = PlayerTrackChrome(player: playback.player)
        let container = PassThroughOverlayView {
            chrome.wake()
        }
        container.translatesAutoresizingMaskIntoConstraints = false
        vc.view.addSubview(container)
        NSLayoutConstraint.activate([
            container.leadingAnchor.constraint(equalTo: vc.view.leadingAnchor),
            container.trailingAnchor.constraint(equalTo: vc.view.trailingAnchor),
            container.topAnchor.constraint(equalTo: vc.view.topAnchor),
            container.bottomAnchor.constraint(equalTo: vc.view.bottomAnchor),
        ])

        let host = UIHostingController(rootView: PlayerTrackPill(playback: playback, chrome: chrome))
        host.view.backgroundColor = .clear
        host.view.translatesAutoresizingMaskIntoConstraints = false
        host.view.accessibilityIdentifier = "ply.player.trackPill"
        vc.addChild(host)
        container.addSubview(host.view)
        NSLayoutConstraint.activate([
            host.view.topAnchor.constraint(equalTo: vc.view.safeAreaLayoutGuide.topAnchor, constant: PlyDesign.overlayTopOffset),
            host.view.trailingAnchor.constraint(equalTo: vc.view.safeAreaLayoutGuide.trailingAnchor, constant: -PlyDesign.playerTrackTrailingOffset),
        ])
        trackHost = host
        host.didMove(toParent: vc)
    }
}

@Observable @MainActor
final class PlayerTrackChrome {
    var visible = true
    private var hideTask: Task<Void, Never>?
    private var statusObservation: NSKeyValueObservation?
    private weak var player: AVPlayer?

    init(player: AVPlayer) {
        self.player = player
        statusObservation = player.observe(\.timeControlStatus, options: [.initial, .new]) { [weak self] p, _ in
            let paused = p.timeControlStatus == .paused
            Task { @MainActor [weak self] in
                guard let self else { return }
                if paused {
                    self.hideTask?.cancel()
                    withAnimation(.easeInOut(duration: 0.2)) { self.visible = true }
                } else {
                    self.scheduleHide()
                }
            }
        }
        scheduleHide()
    }

    func wake() {
        hideTask?.cancel()
        if !visible {
            withAnimation(.easeInOut(duration: 0.2)) { visible = true }
        }
        if player?.timeControlStatus != .paused {
            scheduleHide()
        }
    }

    private func scheduleHide() {
        hideTask?.cancel()
        hideTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(3.5))
            guard !Task.isCancelled, let self else { return }
            if self.player?.timeControlStatus != .paused {
                withAnimation(.easeInOut(duration: 0.25)) { self.visible = false }
            }
        }
    }
}

/// Transparent full-frame overlay that passes all touches outside the compact pill straight to AVKit.
private final class PassThroughOverlayView: UIView {
    private let onActivity: () -> Void

    init(onActivity: @escaping () -> Void) {
        self.onActivity = onActivity
        super.init(frame: .zero)
        backgroundColor = .clear
    }

    required init?(coder: NSCoder) { nil }

    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
        let hit = super.hitTest(point, with: event)
        if let hit, hit !== self {
            onActivity()
            return hit
        }
        if event?.type == .touches {
            onActivity()
        }
        return nil
    }
}

/// Compact frosted glass pill in AVPlayerViewController's top bar for Audio and Subtitles,
/// sharing the same 44pt height, glyph scale, and glass capsule tokens as all overlay controls.
struct PlayerTrackPill: View {
    let playback: PlaybackController
    let chrome: PlayerTrackChrome

    var body: some View {
        let audio = playback.availableAudioStreams
        let subtitles = playback.availableSubtitles
        let hasActiveSubtitle = playback.selectedSubtitle != nil

        HStack(spacing: 4) {
            trackMenu(
                systemImage: "speaker.wave.2",
                color: .white,
                accessibilityLabel: "Audio"
            ) {
                if audio.isEmpty {
                    Text("Default Audio")
                } else {
                    ForEach(audio, id: \.index) { stream in
                        Button {
                            Task { await playback.selectAudio(stream.index) }
                        } label: {
                            if playback.activeAudioIndex == stream.index {
                                Label(stream.label, systemImage: "checkmark")
                            } else {
                                Text(stream.label)
                            }
                        }
                    }
                }
            }

            trackMenu(
                systemImage: hasActiveSubtitle ? "captions.bubble.fill" : "captions.bubble",
                color: hasActiveSubtitle ? .plyAccent : .white,
                accessibilityLabel: "Subtitles"
            ) {
                Button {
                    Task { await playback.selectSubtitle(nil) }
                } label: {
                    if playback.selectedSubtitle == nil {
                        Label("Off", systemImage: "checkmark")
                    } else {
                        Text("Off")
                    }
                }

                if subtitles.isEmpty {
                    Text("No Subtitles Available")
                } else {
                    ForEach(subtitles, id: \.0.key) { sub, choice in
                        let title = sub.image ? "\(sub.label) (burned in)" : sub.label
                        Button {
                            Task { await playback.selectSubtitle(choice) }
                        } label: {
                            if playback.selectedSubtitle == choice {
                                Label(title, systemImage: "checkmark")
                            } else {
                                Text(title)
                            }
                        }
                    }
                }
            }
        }
        .padding(.horizontal, 4)
        .plyGlassCapsule(strokeOpacity: 0.18, interactive: true)
        .shadow(color: .black.opacity(0.35), radius: 8, y: 3)
        .opacity(chrome.visible ? 1 : 0)
        .allowsHitTesting(chrome.visible)
    }

    @ViewBuilder
    private func trackMenu<MenuContent: View>(
        systemImage: String,
        color: Color,
        accessibilityLabel: String,
        @ViewBuilder content: () -> MenuContent
    ) -> some View {
        Menu(content: content) {
            GlassOverlayIconGlyph(systemImage: systemImage, color: color)
        }
        .accessibilityLabel(accessibilityLabel)
    }
}
#elseif os(tvOS)
private extension PlayerPresenter {
    static func tvTransportMenus(for playback: PlaybackController) -> [UIMenuElement] {
        let audioMenu = UIMenu(
            title: "Audio",
            image: UIImage(systemName: "waveform"),
            options: .singleSelection,
            children: [
                UIDeferredMenuElement.uncached { [weak playback] completion in
                    guard let playback else { completion([]); return }
                    let actions = playback.availableAudioStreams.map { stream in
                        UIAction(
                            title: stream.label,
                            state: playback.activeAudioIndex == stream.index ? .on : .off
                        ) { [weak playback] _ in
                            Task { await playback?.selectAudio(stream.index) }
                        }
                    }
                    completion(actions)
                }
            ]
        )

        let subtitleMenu = UIMenu(
            title: "Subtitles",
            image: UIImage(systemName: "captions.bubble"),
            options: .singleSelection,
            children: [
                UIDeferredMenuElement.uncached { [weak playback] completion in
                    guard let playback else { completion([]); return }
                    var actions: [UIAction] = [
                        UIAction(title: "Off", state: playback.selectedSubtitle == nil ? .on : .off) { [weak playback] _ in
                            Task { await playback?.selectSubtitle(nil) }
                        }
                    ]
                    actions += playback.availableSubtitles.map { sub, choice in
                        let title = sub.image ? "\(sub.label) (burned in)" : sub.label
                        return UIAction(title: title, state: playback.selectedSubtitle == choice ? .on : .off) { [weak playback] _ in
                            Task { await playback?.selectSubtitle(choice) }
                        }
                    }
                    completion(actions)
                }
            ]
        )

        return [audioMenu, subtitleMenu]
    }
}
#endif
