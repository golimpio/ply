import Foundation

/// The search matcher of the web UI (`web/src/lib/fuzzy.ts`), ported so that the app ranks a search
/// the same way: every character of the query must appear in the text, in order, and a match at a
/// word's start or in a run of characters scores higher than one scattered through it. Case and
/// accents are folded away in both directions, so "amelie" finds "Amélie" and "POKEMON" "Pokémon".
///
/// A row says once which of its fields a person may type (`fuzzyText`); `Fuzzy.search` then ranks a
/// list, and `Fuzzy.Prepared` does the same from a list whose text is folded once, so that a
/// keystroke only scores it.
nonisolated protocol FuzzySearchable {
    /// The text a search reads: a title, and whatever else names the row (its show, its year).
    var fuzzyText: String { get }
}

nonisolated enum Fuzzy {
    /// Lower-case and strip accents the way the web's `fold` does — a canonical decomposition, its
    /// combining marks dropped, then lower-cased — so that "amelie" finds "Amélie" and "pokemon"
    /// "Pokémon". Not ICU's diacritic folding, which would also match where the web does not (ß as
    /// "ss", ø as "o").
    static func fold(_ text: String) -> String {
        let stripped = text.decomposedStringWithCanonicalMapping.unicodeScalars
            .filter { !isMark($0) }
            .map { String($0) }
            .joined()
        return stripped.lowercased()
    }

    /// A combining mark: the web's `\p{M}` (nonspacing, spacing and enclosing marks).
    private static func isMark(_ scalar: Unicode.Scalar) -> Bool {
        switch scalar.properties.generalCategory {
        case .nonspacingMark, .spacingMark, .enclosingMark: return true
        default: return false
        }
    }

    /// The fields as one text, the empty ones left out: `haystack` in the web's matcher.
    static func haystack(_ parts: [String?]) -> String {
        parts.compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " ")
    }

    /// The score of a row whose text is folded, for a query already folded: 0 when it does not match.
    /// The same arithmetic as the web's, so that both clients put a search in the same order.
    nonisolated static func score(_ query: [Character], in text: [Character]) -> Int {
        guard !query.isEmpty else { return 1 }
        if let at = firstIndex(of: query, in: text) {
            // The whole query in one piece: the earlier it starts, the better; a word's start is worth
            // more than a piece of a word.
            return 1000 - at + (at == 0 || !isWordCharacter(text[at - 1]) ? 200 : 0)
        }
        var score = 0
        var run = 0
        var from = 0
        for character in query {
            guard from <= text.count, let i = text[from...].firstIndex(of: character) else { return 0 }
            run = i == from ? run + 1 : 0
            score += 1 + run * 2 + (i == 0 || !isWordCharacter(text[i - 1]) ? 5 : 0)
            from = i + 1
        }
        return score
    }

    /// A query as it is scored: folded and trimmed, its runs of white space one space. Empty for
    /// nothing worth asking (a blank query matches nothing).
    static func prepare(_ query: String) -> [Character] {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return [] }
        return Array(fold(trimmed).split(whereSeparator: \.isWhitespace).joined(separator: " "))
    }

    /// The rows a query matches, best first, at most `limit` of them. A blank query matches nothing.
    static func search<Row: FuzzySearchable>(_ rows: [Row], query: String, limit: Int = 30) -> [Row] {
        let prepared = prepare(query)
        guard !prepared.isEmpty else { return [] }
        return ranked(rows.map { (row: $0, folded: Array(fold($0.fuzzyText))) }, prepared: prepared, limit: limit)
    }

    /// A list prepared for searching: its text is folded once, here, and a query then only scores it.
    nonisolated struct Prepared<Row: FuzzySearchable> {
        private let rows: [Row]
        private let folded: [[Character]]

        init(_ rows: [Row]) {
            self.rows = rows
            folded = rows.map { Array(Fuzzy.fold($0.fuzzyText)) }
        }

        /// The rows a query matches, best first, at most `limit` of them.
        func search(_ query: String, limit: Int = 30) -> [Row] {
            let prepared = Fuzzy.prepare(query)
            guard !prepared.isEmpty else { return [] }
            return Fuzzy.ranked(zip(rows, folded).map { pair in (row: pair.0, folded: pair.1) }, prepared: prepared, limit: limit)
        }
    }

    /// Scores every row and puts them in the web's order: the best score first, ties in the list's
    /// own order (the web's sort is stable, so that a search reads the same in both clients).
    private static func ranked<Row>(
        _ rows: [(row: Row, folded: [Character])], prepared: [Character], limit: Int
    ) -> [Row] {
        var scored: [(index: Int, score: Int)] = []
        scored.reserveCapacity(rows.count)
        for index in rows.indices {
            let score = score(prepared, in: rows[index].folded)
            if score > 0 { scored.append((index, score)) }
        }
        scored.sort { $0.score == $1.score ? $0.index < $1.index : $0.score > $1.score }
        return scored.prefix(limit).map { rows[$0.index].row }
    }

    /// The first place `needle` appears whole in `hay`, or nil.
    private static func firstIndex(of needle: [Character], in hay: [Character]) -> Int? {
        guard !needle.isEmpty, needle.count <= hay.count else { return nil }
        for start in 0...(hay.count - needle.count) where hay[start] == needle[0] {
            if hay[start..<(start + needle.count)].elementsEqual(needle) { return start }
        }
        return nil
    }

    /// A character the web's `\W` calls not a word character: ASCII letters, digits and "_" are
    /// word characters, everything else (a space, a dash, an accent-folded letter of another script)
    /// starts a word. Written out so that both clients split words the same way.
    private static func isWordCharacter(_ character: Character) -> Bool {
        guard character.unicodeScalars.count == 1, let scalar = character.unicodeScalars.first else { return false }
        switch scalar.value {
        case 48...57, 65...90, 97...122, 95: return true
        default: return false
        }
    }
}

// MARK: - what a search reads of each kind of row

extension Item: FuzzySearchable {
    nonisolated var fuzzyText: String {
        Fuzzy.haystack([title, show, episodeTitle, year.map(String.init)])
    }
}

extension ShowSummary: FuzzySearchable {
    nonisolated var fuzzyText: String {
        Fuzzy.haystack([show, year.map(String.init)])
    }
}

extension PersonCard: FuzzySearchable {
    nonisolated var fuzzyText: String {
        Fuzzy.haystack([name, knownFor])
    }
}

extension GenreCount: FuzzySearchable {
    nonisolated var fuzzyText: String {
        Fuzzy.haystack([name])
    }
}

extension CollectionCard: FuzzySearchable {
    nonisolated var fuzzyText: String {
        Fuzzy.haystack([name])
    }
}
