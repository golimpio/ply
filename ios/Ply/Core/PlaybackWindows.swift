import Foundation

/// A window that can show a play. The registry below holds these weakly, so that the UIKit scene
/// never has to be named in the part of it that is tested.
nonisolated protocol PlaybackWindow: AnyObject {}

/// The windows that can show a play, in the order they appeared: a play nobody started (a command
/// from another device) goes in the newest one still there, and a window the person closed drops
/// out without hiding the windows before it.
nonisolated struct PlaybackWindows {
    private struct Box {
        weak var window: (any PlaybackWindow)?
    }

    private var boxes: [Box] = []

    /// The window is here (again); a window registered twice is listed once.
    mutating func add(_ window: any PlaybackWindow) {
        boxes.removeAll { $0.window == nil }
        guard !contains(window) else { return }
        boxes.append(Box(window: window))
    }

    /// The window is gone, and with it anything it held.
    mutating func remove(_ window: any PlaybackWindow) {
        boxes.removeAll { $0.window == nil || $0.window === window }
    }

    func contains(_ window: any PlaybackWindow) -> Bool {
        boxes.contains { $0.window === window }
    }

    /// The newest window still there. A dead entry at the end does not hide a live one before it.
    var newest: (any PlaybackWindow)? {
        boxes.last { $0.window != nil }?.window
    }

    /// How many windows are still there.
    var count: Int {
        boxes.count { $0.window != nil }
    }
}

/// What a "play" command from another device did, which is what its ack says: only a play this
/// command started answers success, and a window that could take it is not the same as a play.
nonisolated enum PlayCommandResult: Equatable {
    case started
    case noServer
    case noWindow
    case notStarted

    /// - Parameters:
    ///   - hasServer: whether there is a connection to play from.
    ///   - hadWindow: whether a window was there to show it.
    ///   - started: whether the call left a new playback (the one this command may answer for).
    static func after(hasServer: Bool, hadWindow: Bool, started: Bool) -> PlayCommandResult {
        if started { return .started }
        if !hasServer { return .noServer }
        if !hadWindow { return .noWindow }
        return .notStarted
    }

    /// What the ack says went wrong; nil when the play started.
    var failure: String? {
        switch self {
        case .started: nil
        case .noServer: "No server to play from"
        case .noWindow: "No window to play in"
        case .notStarted: "Playback could not start"
        }
    }
}
