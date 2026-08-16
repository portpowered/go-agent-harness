// Package audiofixture loads the repository's verified speech audio corpus.
//
// Load accepts an exact manifest ID, validates the corpus as a closed set,
// verifies the selected file's SHA-256, and returns a 16 kHz mono PCM16
// source that reads in 480-sample frames.
package audiofixture
