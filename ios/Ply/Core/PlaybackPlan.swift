import Foundation

/// The subtitle of a play: a text track (its WebVTT, and the server's key for it) or a bitmap one
/// that only the server can burn in.
nonisolated enum SubtitleChoice: Hashable, Sendable {
    case text(key: String, url: String)
    case burnIn(index: Int)
}

/// How one play is asked of the server: the profile, the receiver's URLs, and the subtitle.
nonisolated struct PlaybackPlan: Equatable, Sendable {
    /// `avplayer` for this device, `airplay` for a TV (MPEG-TS HLS, which older Apple TVs play).
    let profile: String
    /// The AirPlay target of GET /api/receivers the play is for, if one was chosen.
    let receiver: String?
    /// A bitmap subtitle to burn into the video.
    let subtitleIndex: Int?
    /// A text subtitle as a rendition of the AirPlay HLS, which the TV shows.
    let subtitleKey: String?
    /// Whether the app draws the text subtitle itself over the picture (the avplayer profile).
    let drawsSubtitles: Bool
}

nonisolated enum PlaybackPlanning {
    /// Chooses what to ask the server for. A receiver chosen up front, or external playback already
    /// on, means the `airplay` profile; a text subtitle is then the server's rendition, not drawn by
    /// the app. A bitmap subtitle is always burned in (docs/M11-CLIENTS.md, "AirPlay").
    static func plan(subtitle: SubtitleChoice?, receiver: Receiver?, externalPlayback: Bool) -> PlaybackPlan {
        let airPlay = receiver != nil || externalPlayback
        var subtitleIndex: Int?
        var subtitleKey: String?
        var draws = false
        switch subtitle {
        case let .burnIn(index):
            subtitleIndex = index
        case let .text(key, _):
            if airPlay { subtitleKey = key } else { draws = true }
        case nil:
            break
        }
        return PlaybackPlan(profile: airPlay ? "airplay" : "avplayer",
                            receiver: receiver?.id,
                            subtitleIndex: subtitleIndex,
                            subtitleKey: subtitleKey,
                            drawsSubtitles: draws)
    }
}
