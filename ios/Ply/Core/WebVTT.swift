import Foundation

/// A WebVTT track's cues, for drawing a text subtitle over the video: AVPlayer cannot add a
/// sidecar track to a file or a stream it did not come with.
nonisolated struct WebVTT: Sendable {
    struct Cue: Equatable, Sendable {
        let start: Double
        let end: Double
        let text: String
    }

    /// In order of start.
    private(set) var cues: [Cue] = []

    init() {}

    init(_ source: String) {
        let normalised = source.replacingOccurrences(of: "\r\n", with: "\n").replacingOccurrences(of: "\r", with: "\n")
        for block in normalised.components(separatedBy: "\n\n") { add(block: block) }
    }

    /// Adds a block of the track, the lines between two blank lines: a cue, or nothing for a block
    /// without a timing line after its optional identifier (the header, a NOTE, a STYLE).
    mutating func add(block: String) {
        let lines = block.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        guard let at = lines.prefix(2).firstIndex(where: { $0.contains("-->") }),
              let (start, end) = Self.timing(lines[at])
        else { return }
        let text = lines[(at + 1)...].map(Self.stripTags).joined(separator: "\n")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        // Cues mostly come in order: after the last one that starts no later.
        let index = cues.lastIndex { $0.start <= start }.map { $0 + 1 } ?? 0
        cues.insert(Cue(start: start, end: end, text: text), at: index)
    }

    /// The text showing at t: every cue that covers it, the earliest first.
    func text(at t: Double) -> String? {
        // Cues that start after t cannot show; of the others, those that have not ended.
        var lo = 0, hi = cues.count
        while lo < hi {
            let mid = (lo + hi) / 2
            if cues[mid].start <= t { lo = mid + 1 } else { hi = mid }
        }
        let showing = cues[..<lo].filter { t < $0.end }
        return showing.isEmpty ? nil : showing.map(\.text).joined(separator: "\n")
    }

    /// Whether a player should say the track is still downloading at t: it is still arriving, and no
    /// cue read so far starts at or after t, so a line for t may yet come. A pause between lines the
    /// track has already passed is only a pause.
    func isPending(at t: Double, arriving: Bool) -> Bool {
        guard arriving else { return false }
        guard t.isFinite, let last = cues.last else { return true }
        return last.start < t
    }

    /// "00:01:02.500 --> 00:01:04.000 line:90%" as seconds.
    private static func timing(_ line: String) -> (Double, Double)? {
        let parts = line.components(separatedBy: "-->")
        guard parts.count == 2,
              let start = seconds(parts[0].trimmingCharacters(in: .whitespaces)),
              let endField = parts[1].split(separator: " ", omittingEmptySubsequences: true).first,
              let end = seconds(String(endField))
        else { return nil }
        return (start, end)
    }

    /// "hh:mm:ss.ttt" or "mm:ss.ttt" (a comma is taken for the dot, as SRT writes it).
    static func seconds(_ s: String) -> Double? {
        let fields = s.replacingOccurrences(of: ",", with: ".").split(separator: ":")
        guard (2...3).contains(fields.count) else { return nil }
        var total = 0.0
        for field in fields {
            guard let v = Double(field), v >= 0 else { return nil }
            total = total * 60 + v
        }
        return total
    }

    /// The text without its markup (<i>, <c.yellow>, <00:00:01.000>) and with its entities read.
    private static func stripTags(_ line: String) -> String {
        var out = ""
        var inTag = false
        for ch in line {
            switch ch {
            case "<": inTag = true
            case ">" where inTag: inTag = false
            default: if !inTag { out.append(ch) }
            }
        }
        for (entity, ch) in [("&lt;", "<"), ("&gt;", ">"), ("&nbsp;", "\u{00A0}"), ("&lrm;", "\u{200E}"), ("&rlm;", "\u{200F}"), ("&amp;", "&")] {
            out = out.replacingOccurrences(of: entity, with: ch)
        }
        return out
    }
}

/// RFC 3339 times as Go writes them: up to nine digits of fraction, which Foundation's ISO 8601
/// parsers do not all take.
nonisolated enum RFC3339 {
    static func date(_ s: String) -> Date? {
        var whole = s
        var fraction = 0.0
        if let dot = s.firstIndex(of: ".") {
            let digits = s[s.index(after: dot)...].prefix(while: \.isNumber)
            guard !digits.isEmpty else { return nil }
            fraction = Double("0." + digits) ?? 0
            whole = String(s[..<dot]) + String(s[digits.endIndex...])
        }
        guard let date = ISO8601DateFormatter().date(from: whole) else { return nil }
        return date.addingTimeInterval(fraction)
    }
}
