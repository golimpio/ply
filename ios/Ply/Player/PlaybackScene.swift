import AVFoundation
import Observation
import UIKit

/// What a window presents a play with: its own presenter and its own hold on the play, so that two
/// windows never share one player. There is one play at a time across windows; the window that
/// started it shows it, and `AppModel` ends it there (docs/M11-CLIENTS.md, "The apps").
@Observable @MainActor final class PlaybackScene {
    private let presenter: any PlaybackPresenting
    private(set) var presentation = PlaybackPresentation()
    /// What the mini-player drives while the player is stepped aside and the play goes on, on a TV;
    /// nil otherwise (docs/M11-CLIENTS.md, "The mini-player").
    private(set) var miniPlayer: (any RemoteTransport)?
    /// The window this scene belongs to: the player is presented in it, not in whichever window UIKit
    /// calls active. `RootView` sets it from the view it lives in.
    weak var windowScene: UIWindowScene?

    init(presenter: any PlaybackPresenting) {
        self.presenter = presenter
        presenter.onCollapse = { [weak self] remote in self?.collapsed(remote) }
    }

    /// Shows the play in this window; `onDismiss` when the person closes it here.
    func present(_ playback: PlaybackController, onDismiss: @escaping () -> Void) {
        miniPlayer = nil
        presentation.present()
        presenter.present(playback, in: windowScene, onDismiss: onDismiss)
    }

    /// The mini-player was tapped: the player comes back over the window.
    func expand() {
        presenter.expand()
    }

    /// Takes the play down from this window; `onDone` when it is gone.
    func dismiss(onDone: @escaping () -> Void) {
        miniPlayer = nil
        presentation.dismiss()
        presenter.dismiss(completion: onDone)
    }

    /// The window went away with the play on its screen: it is not dismissed (there is nothing to
    /// dismiss into), only forgotten.
    func forget() {
        miniPlayer = nil
        presentation.dismiss()
        presenter.forget()
    }

    /// The player stepped aside for the mini-player (`remote`), or came back (nil).
    private func collapsed(_ remote: (any RemoteTransport)?) {
        guard presentation.shown else { return }
        miniPlayer = remote
        if remote == nil { presentation.expand() } else { presentation.collapse() }
    }
}

/// The window-level half of the player, as a scene needs it: `PlayerPresenter` in the app, a stand-in in
/// a test.
@MainActor protocol PlaybackPresenting: AnyObject {
    /// Shows the play over everything in this window, with the player's controls and gestures.
    func present(_ playback: PlaybackController, in windowScene: UIWindowScene?, onDismiss: @escaping () -> Void)
    /// Takes it down; `completion` when it is gone.
    func dismiss(completion: @escaping () -> Void)
    /// Brings back the player that stepped aside for the mini-player.
    func expand()
    /// The window went away: lets go of its player without dismissing it, so nothing of the play (a
    /// mini-player's player coming back when the TV drops) outlives it.
    func forget()
    /// Told when the player steps aside for the mini-player, with what drives the play on the TV, and
    /// with nil when the player is back on the screen.
    var onCollapse: (((any RemoteTransport)?) -> Void)? { get set }
}

extension PlayerPresenter: PlaybackPresenting {}

/// A scene is one of the windows a play can be shown in, for the registry `AppModel` keeps.
extension PlaybackScene: PlaybackWindow {}
