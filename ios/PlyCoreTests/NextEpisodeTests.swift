import Foundation
import Testing

private func episode(id: Int64, season: Int, number: Int) throws -> Item {
    try APIClient.decode(Data("""
    {"id":\(id),"kind":"episode","title":"E\(number)","show":"Show","season":\(season),"episode":\(number),"durationSec":1}
    """.utf8))
}

@Suite struct NextEpisodeTests {
    @Test func theNextDistinctEpisodeSkipsAnotherFileOfTheSameOne() throws {
        let oneA = try episode(id: 1, season: 1, number: 1)
        let oneB = try episode(id: 2, season: 1, number: 1)
        let two = try episode(id: 3, season: 1, number: 2)
        let three = try episode(id: 4, season: 1, number: 3)
        let list = [three, oneB, two, oneA] // out of order on purpose

        #expect(NextEpisodeSelection.after(oneA, in: list)?.id == 3)
        #expect(NextEpisodeSelection.after(oneB, in: list)?.id == 3)
        #expect(NextEpisodeSelection.after(two, in: list)?.id == 4)
        #expect(NextEpisodeSelection.after(three, in: list) == nil, "the last episode has no next")
    }

    @Test func specialsComeBeforeSeasonOneAndAnUnknownEpisodeHasNoNext() throws {
        let special = try episode(id: 10, season: 0, number: 1)
        let first = try episode(id: 11, season: 1, number: 1)
        #expect(NextEpisodeSelection.after(special, in: [first, special])?.id == 11)

        let stranger = try episode(id: 12, season: 2, number: 2)
        #expect(NextEpisodeSelection.after(stranger, in: [special, first]) == nil)
    }
}
