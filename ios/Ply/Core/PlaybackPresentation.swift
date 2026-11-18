import Foundation

/// A window's hold on the play: whether this window is the one showing it. It is what keeps a second
/// window's player from dismissing the first window's, and what says that a play ended where it was
/// presented (docs/M11-CLIENTS.md, "The apps").
nonisolated struct PlaybackPresentation: Equatable {
    /// Whether this window holds a play: its player on the screen, or stepped aside for the mini-player.
    private(set) var shown = false
    /// The player stepped aside while the play goes on, on a TV: the window still holds the play, and
    /// shows the mini-player in place of the player (docs/M11-CLIENTS.md, "The mini-player").
    private(set) var collapsed = false

    /// Whether the player covers this window's screen, so the app under it takes no touches.
    var onScreen: Bool { shown && !collapsed }

    /// A play is on this window's screen now; one that replaces another keeps it shown.
    mutating func present() {
        shown = true
        collapsed = false
    }

    /// The player steps aside for the mini-player; a window without a play has nothing to collapse.
    mutating func collapse() {
        collapsed = shown
    }

    /// The player is back on this window's screen.
    mutating func expand() {
        collapsed = false
    }

    /// The play left this window's screen (closed, ended, or another window took it).
    mutating func dismiss() {
        shown = false
        collapsed = false
    }
}
