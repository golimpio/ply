import AVFoundation
import Observation
import UIKit

/// What a window presents a play with: its own presenter and its own hold on the play, so that two
/// windows never share one player. There is one play at a time across windows; the window that
/// started it shows it, and `AppModel` ends it there (docs/M11-CLIENTS.md, "The apps").
@Observable @MainActor final class PlaybackScene {
    private let presenter: any PlaybackPresenting
    private(set) var presentation = PlaybackPresentation()
    /// The window this scene belongs to: the player is presented in it, not in whichever window UIKit
    /// calls active. `RootView` sets it from the view it lives in.
    weak var windowScene: UIWindowScene?

    init(presenter: any PlaybackPresenting) {
        self.presenter = presenter
    }

    /// Shows the play in this window; `onDismiss` when the person closes it here.
    func present(_ playback: PlaybackController, onDismiss: @escaping () -> Void) {
        presentation.present()
        presenter.present(playback, in: windowScene, onDismiss: onDismiss)
    }

    /// Takes the play down from this window; `onDone` when it is gone.
    func dismiss(onDone: @escaping () -> Void) {
        presentation.dismiss()
        presenter.dismiss(completion: onDone)
    }

    /// The window went away with the play on its screen: it is not dismissed (there is nothing to
    /// dismiss into), only forgotten.
    func forget() {
        presentation.dismiss()
    }
}

/// The window-level half of the player, as a scene needs it: AVKit's own presenter in the app, a
/// stand-in in a test.
@MainActor protocol PlaybackPresenting: AnyObject {
    /// Shows the play over everything in this window, with AVKit's controls and gestures.
    func present(_ playback: PlaybackController, in windowScene: UIWindowScene?, onDismiss: @escaping () -> Void)
    /// Takes it down; `completion` when it is gone.
    func dismiss(completion: @escaping () -> Void)
}

extension PlayerPresenter: PlaybackPresenting {}
