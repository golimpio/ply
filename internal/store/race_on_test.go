//go:build race

package store

// raceDetector says the tests run under -race, which is several times slower: a timing test gives
// itself more room.
const raceDetector = true
