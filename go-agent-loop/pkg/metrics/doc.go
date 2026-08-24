// Package metrics defines the stream-observation contract used by the agent
// loop's speech-to-speech instrumentation.
//
// A recording contains only its direction, modality, and byte size. The
// package deliberately does not accept or retain payload bytes. Histogram
// buckets are non-cumulative, inclusive upper bounds: a sample is counted in
// the first bucket whose bound is greater than or equal to its size. Samples
// larger than every configured bound are counted in the overflow bucket.
package metrics
