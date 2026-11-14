import AVKit
import SwiftUI
import UIKit

/// Shows a play full screen in AVKit's own player, modally from the top of the app, so that it has
/// AVKit's close button, AirPlay route picker and gestures. Text subtitles are drawn on its content
/// overlay.
final class PlayerPresenter: NSObject, AVPlayerViewControllerDelegate, UIAdaptivePresentationControllerDelegate {
    private var current: AVPlayerViewController?
    private var overlay: UIViewController?
    private var onDismiss: (() -> Void)?
    /// Picture in Picture: the player is off the screen on purpose, so its leaving the window is
    /// not the person closing it. A restore brings the player back; closing the PiP window ends it.
    private var pipActive = false
    private var pipRestoring = false

    /// Shows the play, in place of the one showing; onDismiss is called when the person closes it.
    func present(_ playback: PlaybackController, onDismiss: @escaping () -> Void) {
        let vc = AVPlayerViewController()
        vc.player = playback.player
        vc.delegate = self
        vc.modalPresentationStyle = .fullScreen
        #if os(iOS)
        vc.allowsPictureInPicturePlayback = true
        vc.canStartPictureInPictureAutomaticallyFromInline = true
        vc.updatesNowPlayingInfoCenter = false
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

        let old = current
        current = vc
        self.onDismiss = onDismiss
        let show = { [weak self] in
            Self.afterTransitions {
                guard let self, self.current === vc, let top = Self.top() else { return }
                top.present(vc, animated: true) { [weak self] in
                    vc.presentationController?.delegate = self
                }
            }
        }
        if let old, old.presentingViewController != nil {
            Self.afterTransitions { old.dismiss(animated: false, completion: show) }
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
        onDismiss = nil
        pipActive = false
        pipRestoring = false
        // A play that fails while the player is still coming up is dismissed once it is up.
        Self.afterTransitions {
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
        if playerViewController.presentingViewController == nil, let top = Self.top(), !playerViewController.isBeingPresented {
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
        onDismiss = nil
        done?()
    }

    func presentationControllerDidDismiss(_ presentationController: UIPresentationController) {
        if let vc = presentationController.presentedViewController as? AVPlayerViewController {
            closed(vc)
        }
    }

    private static var root: UIViewController? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let scene = scenes.first { $0.activationState == .foregroundActive } ?? scenes.first
        return scene?.keyWindow?.rootViewController
    }

    private static func top() -> UIViewController? {
        var vc = root
        while let next = vc?.presentedViewController, !next.isBeingDismissed {
            vc = next
        }
        return vc
    }

    /// Runs action once no presentation or dismissal is under way: UIKit drops one asked for during
    /// another (a play that fails as its player comes up, a play started as the last one closes).
    private static func afterTransitions(_ action: @escaping () -> Void) {
        var vc = root
        while let v = vc {
            if let coordinator = v.transitionCoordinator {
                coordinator.animate(alongsideTransition: nil) { _ in afterTransitions(action) }
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
            DispatchQueue.main.async { [onLeave] in onLeave() }
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
