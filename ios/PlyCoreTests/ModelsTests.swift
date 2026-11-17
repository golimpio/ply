import Foundation
import Testing

// Hand-written answers in the shapes internal/api writes (card.go, meta_types.go, types.go,
// commands.go). They pin the decoder and the field names; they are not generated from the Go types,
// so a rename made on both sides at once would not be caught here.

private func decode<T: Decodable>(_ json: String) throws -> T {
    try APIClient.decode(Data(json.utf8))
}

@Suite struct MovieCardTests {
    @Test func aMovieCardIsTheGridRow() throws {
        let item: Item = try decode(#"""
        {"id":12,"kind":"movie","title":"The Matrix","sortTitle":"Matrix, The","year":1999,"titleId":3,
         "addedAt":1700000000,"poster":"/api/images/poster.jpg","backdrop":"/api/images/backdrop.jpg",
         "art":{"thumb":"/api/items/12/art/thumb?v=1"},"rating":{"source":"imdb","value":8.7,"scale":10},
         "watched":false,"progress":{"positionSec":0,"watched":false},"durationSec":8160,
         "container":"mkv","width":1920,"height":1080,"matchState":"matched","needsReview":false}
        """#)
        #expect(item.id == 12)
        #expect(item.title == "The Matrix")
        #expect(item.year == 1999)
        #expect(item.isEpisode == false)
        #expect(item.isWatched == false)
        #expect(item.art?.thumb == "/api/items/12/art/thumb?v=1")
        #expect(item.rating?.label == "IMDb 8.7")
        #expect(item.resumeSec == 0)
        #expect(item.fraction == nil)
        #expect(item.landscapeImage == "/api/images/backdrop.jpg")
    }

    @Test func anEpisodeCardCarriesWhatTheSeasonListDraws() throws {
        let item: Item = try decode(#"""
        {"id":13,"kind":"episode","title":"S01E02","year":1999,"show":"The Matrix","season":1,"episode":2,
         "episodeTitle":"Pilot","addedAt":1700000001,"durationSec":3000,"still":"/api/images/still.jpg",
         "overview":"An episode's own overview.","art":{"thumb":"/api/items/13/art/thumb?v=1"},
         "watched":false,"progress":{"positionSec":1500,"watched":false},"width":1920,"height":1080}
        """#)
        #expect(item.isEpisode)
        #expect(item.episodeCode == "S1 · E2")
        #expect(item.landscapeImage == "/api/images/still.jpg")
        #expect(item.resumeSec == 1500)
        #expect(item.fraction == 0.5)
        #expect(item.isWatched == false)
    }

    @Test func anItemDetailCarriesStreamsSubtitlesAndTheTitlesMeta() throws {
        let detail: ItemDetail = try decode(#"""
        {"id":12,"kind":"movie","title":"The Matrix","year":1999,"addedAt":1700000000,"durationSec":8160,
         "progress":{"positionSec":600,"watched":false},"watched":false,
         "streams":[
           {"index":0,"type":"video","codec":"h264","width":1920,"height":1080},
           {"index":1,"type":"audio","codec":"aac","language":"en","channels":6,"default":true},
           {"index":2,"type":"audio","codec":"aac","language":"fr","channels":2,"default":false},
           {"index":3,"type":"subtitle","codec":"subrip","language":"en","title":"English","forced":false}],
         "subtitles":[
           {"key":"3","language":"en","title":"English","format":"subrip","image":false,"forced":false,
            "default":false,"url":"/api/items/12/subtitles/3.vtt?token=u1.1"},
           {"key":"4","language":"en","title":"Signs","format":"hdmv_pgs_subtitle","image":true,"forced":true,
            "default":false}],
         "meta":{"id":3,"kind":"movie","title":"The Matrix","year":1999,"overview":"A hacker learns the truth.",
           "tagline":"Welcome to the real world.","runtimeMin":136,"certification":"R",
           "rating":{"source":"imdb","value":8.7,"scale":10},"genres":[{"id":28,"name":"Action"}],
           "cast":[{"personId":7,"name":"Keanu Reeves","character":"Neo","profile":"/api/images/p.jpg"}],
           "crew":[{"personId":9,"name":"Lana Wachowski","job":"Director","profile":"/api/images/w.jpg"}],
           "collection":{"id":5,"name":"The Matrix Collection"},
           "collections":[{"id":5,"name":"The Matrix Collection"},{"id":9,"name":"The Wachowskis"}]},
         "episodeInfo":null,
         "playback":{"method":"direct","url":"/api/items/12/file?sid=s1","sessionId":"s1","reasons":[]}}
        """#)
        #expect(detail.item.title == "The Matrix")
        #expect(detail.audioStreams.map(\.index) == [1, 2])
        #expect(detail.audioStreams.first?.default == true)
        #expect(detail.subtitles.count == 2)
        #expect(detail.subtitles[0].url != nil)
        #expect(detail.subtitles[1].image)
        #expect(detail.overview == "A hacker learns the truth.")
        #expect(detail.meta?.collection?.name == "The Matrix Collection")
        #expect(detail.meta?.collections?.count == 2)
        #expect(detail.meta?.collections?.last?.name == "The Wachowskis")
        #expect(detail.meta?.genres?.first?.name == "Action")
        #expect(detail.meta?.cast?.first?.personId == 7)
        #expect(detail.meta?.crew?.first?.name == "Lana Wachowski")
        #expect(detail.meta?.crew?.first?.job == "Director")
    }

    @Test func anEpisodesDetailPrefersItsOwnOverview() throws {
        let detail: ItemDetail = try decode(#"""
        {"id":13,"kind":"episode","title":"S01E02","show":"The Matrix","season":1,"episode":2,
         "durationSec":3000,"overview":"The item's overview.","streams":[],"subtitles":[],
         "meta":{"id":3,"kind":"movie","title":"The Matrix","overview":"The show's overview."},
         "episodeInfo":{"name":"Pilot","overview":"The episode's overview.","airDate":"1999-03-31"}}
        """#)
        #expect(detail.overview == "The episode's overview.")
        #expect(detail.episodeInfo?.name == "Pilot")
    }

    @Test func audioStreamLabelAlwaysIncludesCodecEvenWhenTitled() throws {
        let titled: MediaStream = try decode(#"{"index":1,"type":"audio","codec":"eac3","language":"en","title":"Surround","channels":6,"default":true}"#)
        let redundantTitle: MediaStream = try decode(#"{"index":2,"type":"audio","codec":"eac3","language":"en","title":"EAC3 5.1","channels":6}"#)
        #expect(titled.label.contains("Surround"))
        #expect(titled.label.contains("EAC3"))
        #expect(titled.label.contains("5.1"))
        #expect(redundantTitle.label.components(separatedBy: "EAC3").count == 2)
    }
}

@Suite struct ShowAndMetaTests {
    @Test func aShowDetailCarriesItsSeasons() throws {
        let detail: ShowDetail = try decode(#"""
        {"show":"The Matrix","episodeCount":2,"watchedEpisodes":1,"inProgress":0,"year":1999,
         "overview":"A hacker learns the truth.","poster":"/api/images/poster.jpg","sortTitle":"Matrix, The",
         "matchState":"matched","needsReview":false,"rating":{"source":"imdb","value":8.7,"scale":10},
         "meta":{"id":3,"title":"The Matrix","kind":"movie","sortTitle":"Matrix, The","year":1999},
         "seasons":[{"season":1,"name":"Season 1","episodeCount":2,"watchedEpisodes":1,"inProgress":0}]}
        """#)
        #expect(detail.show == "The Matrix")
        #expect(detail.seasons?.first?.name == "Season 1")
        #expect(detail.seasons?.first?.episodeCount == 2)
        #expect(detail.meta?.title == "The Matrix")
    }

    @Test func aTitleCardIsWhatTheBrowsePagesList() throws {
        let film: TitleCard = try decode(#"""
        {"id":3,"kind":"movie","title":"The Matrix","sortTitle":"Matrix, The","year":1999,
         "poster":"/api/images/p.jpg","backdrop":"/api/images/b.jpg",
         "rating":{"source":"imdb","value":8.7,"scale":10,"score100":87},"itemId":12,"watched":true}
        """#)
        #expect(film.itemId == 12)
        #expect(film.isSeries == false)
        #expect(film.yearText == "1999")
        #expect(film.rating?.source == "imdb")
        #expect(film.rating?.score == 87)
        #expect(film.isWatched, "a film watched in any version")

        let series: TitleCard = try decode(#"""
        {"id":4,"kind":"series","title":"The Matrix","sortTitle":"Matrix, The","year":1999,
         "show":"The Matrix","watched":false,"episodeCount":3,"watchedEpisodes":1,"inProgress":1}
        """#)
        #expect(series.isSeries)
        #expect(series.show == "The Matrix")
        #expect(series.episodeCount == 3)
        #expect(series.isWatched == false)

        let finished: TitleCard = try decode(#"""
        {"id":5,"kind":"series","title":"Ended Show","sortTitle":"Ended Show","year":2001,
         "show":"Ended Show","watched":true,"episodeCount":2,"watchedEpisodes":2,"inProgress":0}
        """#)
        #expect(finished.isWatched)

        let empty: TitleCard = try decode(#"""
        {"id":6,"kind":"series","title":"No Episodes","sortTitle":"No Episodes","year":2001,
         "show":"No Episodes","watched":false,"episodeCount":0}
        """#)
        #expect(empty.isWatched == false)
    }

    @Test func aRatingWithoutScore100FallsBackToItsScale() throws {
        let imdb: Rating = try decode(#"{"source":"imdb","value":8.7,"scale":10}"#)
        #expect(imdb.score == 87)
        let critics: Rating = try decode(#"{"source":"rt_critics","value":92,"scale":100}"#)
        #expect(critics.score == 92)
        let noScale: Rating = try decode(#"{"source":"x","value":1,"scale":0}"#)
        #expect(noScale.score == 0)
    }

    @Test func aPersonHasTheirCredits() throws {
        let person: Person = try decode(#"""
        {"id":7,"name":"Keanu Reeves","knownFor":"John Wick","biography":"A biography.",
         "birthday":"1964-09-02","placeOfBirth":"Beirut, Lebanon","tmdbId":6384,
         "credits":[
           {"titleId":3,"kind":"cast","character":"Neo","title":{"id":3,"kind":"movie","title":"The Matrix",
             "sortTitle":"Matrix, The","year":1999,"itemId":12,"watched":false}},
           {"titleId":4,"kind":"crew","job":"Director","title":{"id":4,"kind":"movie","title":"John Wick",
             "sortTitle":"John Wick","year":2014,"itemId":14,"watched":false}}]}
        """#)
        #expect(person.id == 7)
        #expect(person.credits.count == 2)
        #expect(person.credits[0].character == "Neo")
        #expect(person.credits[0].title.itemId == 12)
        #expect(person.credits[1].job == "Director")
    }

    @Test func aCollectionAndItsTitles() throws {
        let collection: CollectionDetail = try decode(#"""
        {"id":5,"name":"The Matrix Collection","overview":"Wake up.","poster":"/api/images/cp.jpg",
         "backdrop":"/api/images/cb.jpg","titles":[
           {"id":3,"kind":"movie","title":"The Matrix","sortTitle":"Matrix, The","year":1999,"itemId":12,"watched":false}]}
        """#)
        #expect(collection.name == "The Matrix Collection")
        #expect(collection.titles.first?.itemId == 12)

        let cards: [CollectionCard] = try decode(#"""
        [{"id":5,"name":"The Matrix Collection","poster":"/api/images/cp.jpg","count":1}]
        """#)
        #expect(cards.first?.count == 1)
    }

    @Test func aReceiverSaysWhetherTheServerCanRouteToIt() throws {
        let receivers: [Receiver] = try decode(#"""
        [{"id":"AA:BB:CC:DD","name":"Living Room","model":"AppleTV3,2","kind":"airplay",
          "address":"192.168.4.50","port":7000,"base":"http://192.168.4.196:8420"},
         {"id":"EE:FF:00:11","name":"Bedroom","kind":"airplay","address":"10.0.0.9","port":7000}]
        """#)
        #expect(receivers.count == 2)
        #expect(receivers[0].canRoute)
        #expect(receivers[0].base == "http://192.168.4.196:8420")
        #expect(receivers[1].canRoute == false)
    }
}

@Suite struct FeedModelTests {
    @Test func aSnapshotCarriesTheCommandsStillWaiting() throws {
        let snapshot: Snapshot = try decode(#"""
        {"api":1,"commands":[
          {"seq":5,"type":"play","itemId":7,"positionSec":120.5,"from":"dev-2",
           "expiresAt":"2026-11-14T06:55:00.123456789Z"}]}
        """#)
        #expect(snapshot.api == 1)
        let command = try #require(snapshot.commands?.first)
        #expect(command.seq == 5)
        #expect(command.type == "play")
        #expect(command.itemId == 7)
        #expect(command.positionSec == 120.5)
        #expect(command.from == "dev-2")
        #expect(abs(command.expiresAt.timeIntervalSince1970 - 1_794_639_300.123) < 0.01)

        let event: CommandEvent = try decode(#"""
        {"command":{"seq":6,"type":"seek","positionSec":30,"expiresAt":"2026-11-14T06:55:00Z"}}
        """#)
        #expect(event.command.type == "seek")
        #expect(event.command.itemId == nil)
    }

    @Test func aPlaybackAnswerWithNoURLIsRead() throws {
        let playback: Playback = try decode(#"{"method":"transcode","url":"","reasons":["this item cannot be played here"]}"#)
        #expect(playback.url == "")
        #expect(playback.sessionId == nil)
        #expect(playback.reasons?.last == "this item cannot be played here")
    }
}

@Suite struct NewFeaturesModelTests {
    @Test func titleTrailersDecodeAndState() throws {
        let two: TitleTrailers = try decode(#"""
        {"local":[{"itemId":8,"name":"Vivre sa vie (1962)-trailer","durationSec":128.5},{"itemId":9,"name":"Teaser-trailer","durationSec":60}],
         "download":{"state":"running","jobId":3}}
        """#)
        #expect(two.buttonState == .play(two.local))
        #expect(two.local[0].id == 8)
        let queued: TitleTrailers = try decode(#"{"local":[],"download":{"state":"queued","jobId":3}}"#)
        #expect(queued.buttonState == .downloading)
        let running: TitleTrailers = try decode(#"{"local":[],"download":{"state":"running","jobId":3}}"#)
        #expect(running.buttonState == .downloading)
        let failed: TitleTrailers = try decode(#"{"local":[],"download":{"state":"failed","jobId":3,"error":"YouTube: Sign in"}}"#)
        #expect(failed.buttonState == .none)
        let none: TitleTrailers = try decode(#"{"local":[]}"#)
        #expect(none.buttonState == .none)
    }

    @Test func upcomingShowsDecodeWithNextEpisode() throws {
        let upcoming: [UpcomingShow] = try decode(#"""
        [{"titleId":78,"show":"Dark","poster":"/api/p.jpg","backdrop":"/api/b.jpg",
          "nextAirDate":"2026-11-20","nextEpisode":{"season":2,"episode":1,"name":"Beginnings"}}]
        """#)
        #expect(upcoming.count == 1)
        #expect(upcoming[0].show == "Dark")
        #expect(upcoming[0].nextAirDate == "2026-11-20")
        #expect(upcoming[0].nextEpisode?.season == 2)
        #expect(upcoming[0].nextEpisode?.episode == 1)
        #expect(upcoming[0].nextEpisode?.name == "Beginnings")
    }

    @Test func personFilmographyDecodes() throws {
        let entries: [PersonFilmographyEntry] = try decode(#"""
        [{"kind":"movie","tmdbId":603,"title":"The Matrix","year":1999,"character":"Neo","creditKind":"cast","rating":8.7,"inLibrary":true},
         {"kind":"series","tmdbId":1399,"title":"Game of Thrones","year":2011,"job":"Executive Producer","creditKind":"crew","inLibrary":false}]
        """#)
        #expect(entries.count == 2)
        #expect(entries[0].title == "The Matrix")
        #expect(entries[0].isSeries == false)
        #expect(entries[0].yearText == "1999")
        #expect(entries[0].roleDescription == "Neo")
        #expect(entries[0].isExternal == false)
        #expect(entries[1].isSeries)
        #expect(entries[1].roleDescription == "Executive Producer")
        #expect(entries[1].isExternal)
    }

    @Test func movieVersionLabelAndSpecs() throws {
        let item: Item = try decode(#"""
        {"id":12,"kind":"movie","title":"The Matrix","container":"mkv","width":3840,"height":2160,"hdr":"hdr10","durationSec":7200}
        """#)
        #expect(item.is4K)
        #expect(item.isHDR)
        #expect(item.versionLabel == "4K HDR10, MKV")
    }

    @Test func personMetadataLinksDatesAndMediaSplitting() throws {
        let person: Person = try decode(#"""
        {"id":42,"name":"Jean-Paul Belmondo","knownFor":"Acting","biography":"French actor.",
         "birthday":"1933-04-09","deathday":"2021-09-06","placeOfBirth":"Neuilly-sur-Seine, France",
         "imdbId":"nm0000901","wikidataId":"Q106255","tmdbId":3784,
         "credits":[
           {"titleId":1,"kind":"cast","character":"Michel","title":{"id":1,"kind":"movie","title":"Breathless","sortTitle":"Breathless","year":1960,"itemId":10}},
           {"titleId":2,"kind":"cast","character":"Alfred","title":{"id":2,"kind":"movie","title":"A Woman Is a Woman","sortTitle":"Woman Is a Woman, A","year":1961,"itemId":11}},
           {"titleId":2,"kind":"crew","job":"Director","title":{"id":2,"kind":"movie","title":"A Woman Is a Woman","sortTitle":"Woman Is a Woman, A","year":1961,"itemId":11}},
           {"titleId":3,"kind":"cast","character":"Cameo","title":{"id":3,"kind":"series","title":"Some Show","sortTitle":"Some Show","year":1970,"show":"Some Show"}},
           {"titleId":4,"kind":"cast","character":"Unlinked","title":{"id":4,"kind":"movie","title":"No File","sortTitle":"No File","year":1975}}
         ]}
        """#)

        #expect(person.lifeSpan == "1933–2021")
        #expect(person.wikipediaURL?.absoluteString == "https://www.wikidata.org/wiki/Special:GoToLinkedPage/enwiki/Q106255")
        #expect(person.imdbURL?.absoluteString == "https://www.imdb.com/name/nm0000901/")

        let media = person.openableMediaCredits
        #expect(CreditSection.uniqueTitleCount(media.films) == 2)
        #expect(CreditSection.uniqueTitleCount(media.series) == 1)

        let filmSections = CreditSection.sections(media.films)
        #expect(filmSections.map(\.group) == [.acting, .directing])
        #expect(filmSections[0].entries.map(\.card.title) == ["A Woman Is a Woman", "Breathless"])

        let filmography: [PersonFilmographyEntry] = try decode(#"""
        [{"kind":"movie","tmdbId":603,"title":"Breathless","year":1960,"inLibrary":true},
         {"kind":"movie","tmdbId":999,"title":"Pierrot le Fou","year":1965,"rating":7.6,"imdbId":"tt0059592","inLibrary":false},
         {"kind":"series","tmdbId":888,"title":"TV Special","year":1968,"episodeCount":3,"inLibrary":false}]
        """#)
        let external = person.externalFilmography(from: filmography)
        #expect(external.films.map(\.title) == ["Pierrot le Fou"])
        #expect(external.films.first?.ratingLabel == "7.6")
        #expect(external.films.first?.imdbURL?.absoluteString == "https://www.imdb.com/title/tt0059592/")
        #expect(external.films.first?.tmdbURL?.absoluteString == "https://www.themoviedb.org/movie/999")
        #expect(external.series.map(\.title) == ["TV Special"])
        #expect(external.series.first?.tmdbURL?.absoluteString == "https://www.themoviedb.org/tv/888")
    }

    @Test func personDateFormattingAndWikiSearchFallback() {
        #expect(PersonDates.lifeSpan(birthday: "1993-08-18", deathday: nil) == "Born 18 August 1993")
        #expect(PersonDates.lifeSpan(birthday: "1961-03", deathday: nil) == "Born March 1961")
        #expect(PersonDates.lifeSpan(birthday: "1961", deathday: nil) == "Born 1961")
        #expect(PersonDates.lifeSpan(birthday: nil, deathday: "2023-07-04") == "Died 4 July 2023")
        #expect(PersonDates.lifeSpan(birthday: "1990-13-01", deathday: nil) == nil)

        let fallback = ExternalLinks.wikipediaURL(name: "Maia Mitchell", wikidataId: nil)
        #expect(fallback?.absoluteString == "https://en.wikipedia.org/wiki/Special:Search?search=Maia%20Mitchell")
    }

    @Test func titlePreviewDecodesAndBuildsCreditLines() throws {
        let preview: TitlePreview = try decode(#"""
        {"kind":"movie","tmdbId":999,"imdbId":"tt0059592","title":"Pierrot le Fou","originalTitle":"Pierrot le fou",
         "year":1965,"overview":"Ferdinand flees Paris.","tagline":"Godard in color.",
         "runtimeMin":110,"certification":"NR","genres":["Crime","Drama"],
         "cast":[{"tmdbId":3784,"name":"Jean-Paul Belmondo","role":"Ferdinand Griffon","profile":"/api/images/jp.jpg"}],
         "crew":[{"name":"Jean-Luc Godard","role":"Director"},
                 {"name":"Jean-Luc Godard","role":"Screenplay"},
                 {"name":"Georges Delerue","role":"Original Music Composer"},
                 {"name":"Raoul Coutard","role":"Director of Photography"}]}
        """#)
        #expect(preview.title == "Pierrot le Fou")
        #expect(preview.genres == ["Crime", "Drama"])
        #expect(preview.cast.count == 1)
        let lines = preview.creditLines
        #expect(lines.map(\.label) == ["Director", "Writer", "Music", "Cinematography"])
        #expect(lines[0].names == ["Jean-Luc Godard"])
        #expect(lines[1].names == ["Jean-Luc Godard"])
        #expect(lines[2].names == ["Georges Delerue"])
        #expect(lines[3].names == ["Raoul Coutard"])
    }
}

@Suite struct TrailerItemTests {
    @Test func aTrailerItemIsATrailer() throws {
        let it: Item = try decode(#"{"id":50,"kind":"trailer","title":"Vivre sa vie","durationSec":128}"#)
        #expect(it.isTrailer && !it.isEpisode)
    }
}
