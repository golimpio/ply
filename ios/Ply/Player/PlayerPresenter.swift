import AVKit
import SwiftUI
import UIKit

/// Shows a play full screen, modally from the top of the app: on iPhone and iPad in ply's own player
/// (`PlayerViewController`: the picture, Picture in Picture and ply's controls), on Apple TV in AVKit's,
/// with text subtitles drawn on its content overlay (docs/M11-CLIENTS.md, "The player").
final class PlayerPresenter: NSObject, UIAdaptivePresentationControllerDelegate {
    private var current: UIViewController?
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
        let vc = makePlayer(for: playback)
        vc.modalPresentationStyle = .fullScreen
        pipActive = false
        pipRestoring = false
        vc.loadViewIfNeeded()
        // Should the player not say it was closed, its leaving the screen says it: a play nobody can
        // see must not be kept alive by its heartbeats. Picture in Picture is the exception: the play
        // is meant to be off the screen. It sits on the player's own view, not on AVKit's content
        // overlay, because tvOS does not always vend one.
        vc.view.addSubview(WindowWatcher { [weak self, weak vc] in
            guard let self, let vc, !self.pipActive, !self.pipRestoring,
                  vc.presentingViewController == nil || vc.isBeingDismissed else { return }
            self.closed(vc)
        })

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
                    Self.tearDown(vc)
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
        if let old { Self.tearDown(old) }
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
        onDismiss = nil
        pipActive = false
        pipRestoring = false
        Self.tearDown(vc)
        // A play that fails while the player is still coming up is dismissed once it is up.
        afterTransitions {
            if vc.presentingViewController != nil {
                vc.dismiss(animated: true, completion: completion)
            } else {
                completion()
            }
        }
    }

    /// The person closed the player showing.
    private func closed(_ vc: UIViewController) {
        guard current === vc else { return }
        let done = onDismiss
        current = nil
        onDismiss = nil
        Self.tearDown(vc)
        done?()
    }

    func presentationControllerDidDismiss(_ presentationController: UIPresentationController) {
        closed(presentationController.presentedViewController)
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

#if os(iOS)
// MARK: - iPhone and iPad: ply's own player

extension PlayerPresenter: AVPictureInPictureControllerDelegate {
    private func makePlayer(for playback: PlaybackController) -> UIViewController {
        let vc = PlayerViewController(playback: playback, pictureInPictureDelegate: self)
        vc.onClose = { [weak self, weak vc] in
            guard let self, let vc else { return }
            self.userClosed(vc)
        }
        return vc
    }

    private static func tearDown(_ vc: UIViewController) {
        (vc as? PlayerViewController)?.tearDown()
    }

    /// The close button, a swipe down, or Stop on the remote: the play ends and the player goes.
    private func userClosed(_ vc: PlayerViewController) {
        guard current === vc else { return }
        closed(vc)
        afterTransitions {
            if vc.presentingViewController != nil { vc.dismiss(animated: true) }
        }
    }

    private func player(for controller: AVPictureInPictureController) -> PlayerViewController? {
        guard let vc = current as? PlayerViewController, vc.pictureInPicture.controller === controller else { return nil }
        return vc
    }

    /// The player is about to leave the screen for the PiP window: a play that is not closed.
    func pictureInPictureControllerWillStartPictureInPicture(_ controller: AVPictureInPictureController) {
        guard player(for: controller) != nil else { return }
        pipActive = true
    }

    /// The picture is in the PiP window: the player steps aside so the app can be browsed, as AVKit's did.
    func pictureInPictureControllerDidStartPictureInPicture(_ controller: AVPictureInPictureController) {
        guard let vc = player(for: controller) else { return }
        afterTransitions {
            if vc.presentingViewController != nil, !vc.isBeingDismissed { vc.dismiss(animated: true) }
        }
    }

    func pictureInPictureController(_ controller: AVPictureInPictureController,
                                    failedToStartPictureInPictureWithError error: any Error) {
        guard player(for: controller) != nil else { return }
        pipActive = false
    }

    /// The person tapped the button that brings the player back: present it again and keep playing.
    func pictureInPictureController(_ controller: AVPictureInPictureController,
                                    restoreUserInterfaceForPictureInPictureStopWithCompletionHandler completionHandler: @escaping (Bool) -> Void) {
        guard let vc = player(for: controller) else {
            completionHandler(false)
            return
        }
        pipRestoring = true
        // A restore tapped while the player is still stepping aside for PiP waits for that to finish:
        // a player mid-dismissal still has a presenter, but is about to be gone.
        afterTransitions { [weak self, weak vc] in
            guard let self else {
                completionHandler(false)
                return
            }
            guard let vc, self.current === vc else {
                self.pipRestoring = false
                completionHandler(false)
                return
            }
            if vc.presentingViewController != nil {
                completionHandler(true)
            } else if let top = self.top() {
                top.present(vc, animated: true) { [weak self, weak vc] in
                    vc?.presentationController?.delegate = self
                    completionHandler(true)
                }
            } else {
                // No window to restore it into: let the system stop the PiP stream.
                self.pipRestoring = false
                completionHandler(false)
            }
        }
    }

    /// The PiP window is gone. If the person asked to come back, the player is up again; otherwise
    /// they closed the window, which ends the play.
    func pictureInPictureControllerDidStopPictureInPicture(_ controller: AVPictureInPictureController) {
        guard let vc = player(for: controller) else {
            pipActive = false
            pipRestoring = false
            return
        }
        pipActive = false
        if pipRestoring {
            pipRestoring = false
            return
        }
        closed(vc)
    }
}

/// The picture: a view whose own layer is the player's `AVPlayerLayer`.
final class PlayerLayerView: UIView {
    override class var layerClass: AnyClass { AVPlayerLayer.self }
    var playerLayer: AVPlayerLayer { layer as! AVPlayerLayer }
}

/// The play full screen on iPhone and iPad: the picture, Picture in Picture on its layer, and
/// `PlayerControls` over it.
final class PlayerViewController: UIViewController {
    let playback: PlaybackController
    let transport: PlayerTransport
    let chrome: PlayerChrome
    let pictureInPicture = PlayerPictureInPicture()
    let pictureView = PlayerLayerView()
    var onClose: (() -> Void)?
    private weak var pictureInPictureDelegate: AVPictureInPictureControllerDelegate?

    init(playback: PlaybackController, pictureInPictureDelegate: AVPictureInPictureControllerDelegate) {
        self.playback = playback
        self.pictureInPictureDelegate = pictureInPictureDelegate
        transport = PlayerTransport(playback: playback)
        chrome = PlayerChrome(player: playback.player)
        super.init(nibName: nil, bundle: nil)
    }

    required init?(coder: NSCoder) { nil }

    override func loadView() {
        let root = UIView()
        root.backgroundColor = .black
        view = root

        pictureView.playerLayer.player = playback.player
        pictureView.playerLayer.videoGravity = .resizeAspect
        pictureView.frame = root.bounds
        pictureView.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        root.addSubview(pictureView)
        if let pictureInPictureDelegate {
            pictureInPicture.attach(pictureView.playerLayer, delegate: pictureInPictureDelegate)
        }

        let controls = PlayerControls(
            transport: transport,
            chrome: chrome,
            pictureInPicture: pictureInPicture,
            onClose: { [weak self] in self?.onClose?() },
            onFill: { [weak self] fill in self?.setFill(fill) }
        )
        let host = UIHostingController(rootView: controls.preferredColorScheme(.dark))
        host.view.backgroundColor = .clear
        host.view.frame = root.bounds
        host.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        addChild(host)
        root.addSubview(host.view)
        host.didMove(toParent: self)
    }

    override var prefersStatusBarHidden: Bool { true }
    override var prefersHomeIndicatorAutoHidden: Bool { true }
    override var supportedInterfaceOrientations: UIInterfaceOrientationMask {
        traitCollection.userInterfaceIdiom == .pad ? .all : .allButUpsideDown
    }

    /// A pinch out fills the screen (cropping the picture's edges); a pinch in shows all of it.
    func setFill(_ fill: Bool) {
        let gravity: AVLayerVideoGravity = fill ? .resizeAspectFill : .resizeAspect
        guard pictureView.playerLayer.videoGravity != gravity else { return }
        CATransaction.begin()
        CATransaction.setAnimationDuration(0.25)
        pictureView.playerLayer.videoGravity = gravity
        CATransaction.commit()
    }

    /// The play is over: let go of the player's observers.
    func tearDown() {
        transport.detach()
        chrome.detach()
        pictureInPicture.detach()
    }
}

#elseif os(tvOS)
// MARK: - Apple TV: AVKit's player

extension PlayerPresenter: AVPlayerViewControllerDelegate {
    private func makePlayer(for playback: PlaybackController) -> UIViewController {
        let vc = AVPlayerViewController()
        vc.player = playback.player
        vc.delegate = self
        vc.transportBarCustomMenuItems = Self.tvTransportMenus(for: playback)
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
            host.didMove(toParent: vc)
        }
        return vc
    }

    private static func tearDown(_ vc: UIViewController) {}

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
/// says so; one that failed says that instead of silently never appearing. `lift` raises it above the
/// player's controls while they show.
struct SubtitleOverlay: View {
    let playback: PlaybackController
    var lift: CGFloat = 56

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
                    .padding(.bottom, lift)
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
                .padding(.bottom, lift)
            } else if playback.subtitleError != nil {
                Text("Subtitles could not be loaded")
                    .font(.caption)
                    .foregroundStyle(.white)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(.black.opacity(0.45), in: .rect(cornerRadius: 6))
                    .padding(.bottom, lift)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .allowsHitTesting(false)
    }
}
