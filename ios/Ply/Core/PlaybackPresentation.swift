import Foundation

/// A window's hold on the play: whether this window is the one showing it. It is what keeps a second
/// window's player from dismissing the first window's, and what says that a play ended where it was
/// presented (docs/M11-CLIENTS.md, "The apps").
nonisolated struct PlaybackPresentation: Equatable {
    /// Whether a play is on this window's screen.
    private(set) var shown = false

    /// A play is on this window's screen now; one that replaces another keeps it shown.
    mutating func present() {
        shown = true
    }

    /// The play left this window's screen (closed, ended, or another window took it).
    mutating func dismiss() {
        shown = false
    }
}
