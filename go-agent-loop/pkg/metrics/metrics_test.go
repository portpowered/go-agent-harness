package metrics

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestValidationErrorsCoverEveryInputAndConfigurationBranch(t *testing.T) {
	type constructorCase struct {
		name        string
		bounds      [][]int64
		wantKind    error
		wantMessage string
	}
	constructorCases := []constructorCase{
		{
			name:        "empty bounds",
			bounds:      [][]int64{{}},
			wantKind:    ErrEmptyHistogramBounds,
			wantMessage: "metrics: histogram bounds must not be empty",
		},
		{
			name:        "negative bound",
			bounds:      [][]int64{{-1, 1}},
			wantKind:    ErrInvalidHistogramBound,
			wantMessage: "metrics: histogram bound at index 0 must be non-negative: -1",
		},
		{
			name:        "duplicate bound",
			bounds:      [][]int64{{1, 1}},
			wantKind:    ErrDuplicateHistogramBound,
			wantMessage: "metrics: histogram bound at index 1 duplicates 1",
		},
		{
			name:        "decreasing bound",
			bounds:      [][]int64{{2, 1}},
			wantKind:    ErrNonIncreasingHistogramBounds,
			wantMessage: "metrics: histogram bound at index 1 is less than previous bound 2: 1",
		},
		{
			name:        "multiple bound slices",
			bounds:      [][]int64{{1}, {2}},
			wantKind:    ErrInvalidHistogramConfiguration,
			wantMessage: "metrics: histogram bounds configuration accepts at most one slice",
		},
	}
	for _, testCase := range constructorCases {
		t.Run(testCase.name, func(t *testing.T) {
			var sink *InMemorySink
			var err error
			if len(testCase.bounds) == 2 {
				sink, err = NewInMemorySink(testCase.bounds[0], testCase.bounds[1])
			} else {
				sink, err = NewInMemorySink(testCase.bounds[0])
			}
			assertValidationError(t, err, testCase.wantKind, testCase.wantMessage)
			if sink != nil {
				t.Fatalf("invalid configuration returned a sink")
			}
		})
	}

	sink, err := NewInMemorySink([]int64{0, 10})
	if err != nil {
		t.Fatalf("construct valid sink: %v", err)
	}
	before := sink.Snapshot()
	recordCases := []struct {
		name        string
		direction   Direction
		modality    Modality
		bytes       int64
		wantKind    error
		wantMessage string
	}{
		{
			name:        "invalid direction",
			direction:   Direction("sideways"),
			modality:    ModalityAudio,
			bytes:       1,
			wantKind:    ErrInvalidDirection,
			wantMessage: `metrics: invalid direction "sideways"`,
		},
		{
			name:        "invalid modality",
			direction:   DirectionInput,
			modality:    Modality("video"),
			bytes:       1,
			wantKind:    ErrInvalidModality,
			wantMessage: `metrics: invalid modality "video"`,
		},
		{
			name:        "negative byte size",
			direction:   DirectionInput,
			modality:    ModalityAudio,
			bytes:       -1,
			wantKind:    ErrInvalidByteSize,
			wantMessage: "metrics: byte size must be non-negative: -1",
		},
	}
	for _, testCase := range recordCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := sink.Record(testCase.direction, testCase.modality, testCase.bytes)
			assertValidationError(t, err, testCase.wantKind, testCase.wantMessage)
			if got := sink.Snapshot(); !reflect.DeepEqual(got, before) {
				t.Fatalf("rejected recording changed snapshot: before=%+v after=%+v", before, got)
			}
		})
	}
}

func assertValidationError(t *testing.T, err, wantKind error, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error: got nil, want %v", wantKind)
	}
	if !errors.Is(err, wantKind) {
		t.Fatalf("errors.Is(%v, %v) = false", err, wantKind)
	}
	var typed *ValidationError
	if !errors.As(err, &typed) {
		t.Fatalf("error %T is not a ValidationError", err)
	}
	if typed.Kind != wantKind {
		t.Fatalf("validation kind: got %v, want %v", typed.Kind, wantKind)
	}
	if err.Error() != wantMessage {
		t.Fatalf("error message: got %q, want %q", err.Error(), wantMessage)
	}
}

func TestHistogramConfigurationAndSnapshotCopies(t *testing.T) {
	bounds := []int64{0, 10, 100}
	sink, err := NewInMemorySinkWithBounds(bounds)
	if err != nil {
		t.Fatalf("construct sink: %v", err)
	}
	bounds[0] = 999
	if got := sink.Snapshot().HistogramBounds[0]; got != 0 {
		t.Fatalf("sink retained caller bounds: got %d", got)
	}

	defaults := DefaultHistogramBounds()
	defaults[0] = 999
	if got := DefaultHistogramBounds()[0]; got != 0 {
		t.Fatalf("default bounds are mutable: got %d", got)
	}

	if err := sink.Record(DirectionInput, ModalityText, 10); err != nil {
		t.Fatalf("record: %v", err)
	}
	first := sink.Snapshot()
	second := sink.Snapshot()
	first.HistogramBounds[0] = 777
	first.Series[0].Histogram.Bounds[0] = 777
	first.Series[0].Histogram.BucketCounts[0] = 777
	if second.HistogramBounds[0] != 0 || second.Series[0].Histogram.Bounds[0] != 0 || second.Series[0].Histogram.BucketCounts[0] != 0 {
		t.Fatalf("snapshots share mutable slices: first=%+v second=%+v", first, second)
	}
}

func TestSnapshotLookupAndNilSink(t *testing.T) {
	sink, err := NewInMemorySink()
	if err != nil {
		t.Fatalf("construct default sink: %v", err)
	}
	snapshot := sink.Snapshot()
	if _, ok := snapshot.Lookup(Direction("unknown"), ModalityAudio); ok {
		t.Fatalf("unknown series unexpectedly found")
	}
	if got := snapshot.Get(Direction("unknown"), ModalityAudio); got.EventCount != 0 || got.TotalBytes != 0 {
		t.Fatalf("missing series returned non-zero snapshot: %+v", got)
	}

	var nilSink *InMemorySink
	if !errors.Is(nilSink.Record(DirectionInput, ModalityAudio, 1), ErrNilSink) {
		t.Fatalf("nil Record did not return ErrNilSink")
	}
	if got := nilSink.Snapshot(); !reflect.DeepEqual(got, Snapshot{}) {
		t.Fatalf("nil Snapshot: got %+v", got)
	}
}

func TestTypedErrorsAndSeriesKeyFormatting(t *testing.T) {
	key := SeriesKey{Direction: DirectionInput, Modality: ModalityAudio}
	if got := key.String(); got != "input/audio" {
		t.Fatalf("series key string: got %q", got)
	}

	var validation *ValidationError
	if got := validation.Error(); got != "<nil>" {
		t.Fatalf("nil validation error: got %q", got)
	}
	if validation.Is(ErrInvalidObservation) {
		t.Fatalf("nil validation error matched an error class")
	}

	validation = &ValidationError{Kind: ErrInvalidDirection, Message: "direction"}
	if !errors.Is(validation, ErrInvalidObservation) || errors.Is(validation, ErrInvalidHistogramConfiguration) {
		t.Fatalf("validation broad classes: observation=%v configuration=%v", errors.Is(validation, ErrInvalidObservation), errors.Is(validation, ErrInvalidHistogramConfiguration))
	}
	validation = &ValidationError{Kind: ErrEmptyHistogramBounds, Message: "bounds"}
	if !errors.Is(validation, ErrInvalidHistogramConfiguration) || errors.Is(validation, ErrInvalidObservation) {
		t.Fatalf("configuration broad classes: observation=%v configuration=%v", errors.Is(validation, ErrInvalidObservation), errors.Is(validation, ErrInvalidHistogramConfiguration))
	}
	validation = &ValidationError{Kind: errors.New("unclassified"), Message: "unclassified"}
	if validation.Is(ErrInvalidObservation) || validation.Is(ErrInvalidHistogramConfiguration) {
		t.Fatalf("unclassified validation error matched a broad class")
	}

	var overflow *CounterOverflowError
	if got := overflow.Error(); got != "<nil>" {
		t.Fatalf("nil overflow error: got %q", got)
	}
	if overflow.Is(ErrCounterOverflow) {
		t.Fatalf("nil overflow error matched ErrCounterOverflow")
	}
	overflow = &CounterOverflowError{Key: key, Field: "event count"}
	if got := overflow.Error(); got != "metrics: counter overflow for input/audio event count" {
		t.Fatalf("overflow error: got %q", got)
	}
	if !errors.Is(overflow, ErrCounterOverflow) {
		t.Fatalf("overflow error did not match ErrCounterOverflow")
	}
}

func TestCounterOverflowLeavesSeriesUnchanged(t *testing.T) {
	type overflowCase struct {
		name   string
		setup  func(*seriesState)
		bounds []int64
		bytes  int64
	}
	cases := []overflowCase{
		{name: "event count", setup: func(state *seriesState) { state.eventCount = maxUint64 }, bounds: []int64{0}, bytes: 0},
		{name: "total bytes", setup: func(state *seriesState) { state.totalBytes = maxUint64 }, bounds: []int64{0}, bytes: 1},
		{name: "sample count", setup: func(state *seriesState) { state.sampleCount = maxUint64 }, bounds: []int64{0}, bytes: 0},
		{name: "histogram byte sum", setup: func(state *seriesState) { state.byteSum = maxUint64 }, bounds: []int64{0}, bytes: 1},
		{name: "bucket count", setup: func(state *seriesState) { state.bucketCounts[0] = maxUint64 }, bounds: []int64{0}, bytes: 0},
		{name: "overflow count", setup: func(state *seriesState) { state.overflowCount = maxUint64 }, bounds: []int64{0}, bytes: 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sink, err := NewInMemorySink(testCase.bounds)
			if err != nil {
				t.Fatalf("construct sink: %v", err)
			}
			key := SeriesKey{Direction: DirectionInput, Modality: ModalityAudio}
			sink.mu.Lock()
			testCase.setup(sink.series[key])
			sink.mu.Unlock()
			before := sink.Snapshot()
			if err := sink.Record(key.Direction, key.Modality, testCase.bytes); !errors.Is(err, ErrCounterOverflow) {
				t.Fatalf("record error: got %v, want ErrCounterOverflow", err)
			}
			if got := sink.Snapshot(); !reflect.DeepEqual(got, before) {
				t.Fatalf("overflow changed snapshot: before=%+v after=%+v", before, got)
			}
		})
	}
}

func TestConcurrentRecordersReconcileExactly(t *testing.T) {
	sink, err := NewInMemorySink([]int64{0, 10, 100, 1000})
	if err != nil {
		t.Fatalf("construct sink: %v", err)
	}
	keys := []SeriesKey{
		{Direction: DirectionInput, Modality: ModalityAudio},
		{Direction: DirectionInput, Modality: ModalityText},
		{Direction: DirectionOutput, Modality: ModalityAudio},
		{Direction: DirectionOutput, Modality: ModalityText},
	}
	const workers = 24
	const observationsPerWorker = 125
	type totals struct {
		count uint64
		bytes uint64
	}
	expected := make(map[SeriesKey]totals)
	for worker := 0; worker < workers; worker++ {
		key := keys[worker%len(keys)]
		for observation := 0; observation < observationsPerWorker; observation++ {
			byteSize := int64((worker+1)*3 + observation%17)
			total := expected[key]
			total.count++
			total.bytes += uint64(byteSize)
			expected[key] = total
		}
	}

	start := make(chan struct{})
	done := make(chan struct{})
	var readerDone sync.WaitGroup
	readerDone.Add(1)
	go func() {
		defer readerDone.Done()
		<-start
		for {
			select {
			case <-done:
				return
			default:
				_ = sink.Snapshot()
			}
		}
	}()

	var producers sync.WaitGroup
	producers.Add(workers)
	for worker := 0; worker < workers; worker++ {
		worker, key := worker, keys[worker%len(keys)]
		go func() {
			defer producers.Done()
			<-start
			for observation := 0; observation < observationsPerWorker; observation++ {
				byteSize := int64((worker+1)*3 + observation%17)
				if err := sink.Record(key.Direction, key.Modality, byteSize); err != nil {
					t.Errorf("record worker %d observation %d: %v", worker, observation, err)
					return
				}
			}
		}()
	}
	close(start)
	producers.Wait()
	close(done)
	readerDone.Wait()

	snapshot := sink.Snapshot()
	for key, want := range expected {
		got := snapshot.SeriesFor(key.Direction, key.Modality)
		if got.EventCount != want.count || got.TotalBytes != want.bytes {
			t.Errorf("%s totals: got count=%d bytes=%d, want count=%d bytes=%d", key, got.EventCount, got.TotalBytes, want.count, want.bytes)
		}
		if got.Histogram.SampleCount != want.count || got.Histogram.ByteSum != want.bytes {
			t.Errorf("%s histogram totals: got samples=%d bytes=%d, want samples=%d bytes=%d", key, got.Histogram.SampleCount, got.Histogram.ByteSum, want.count, want.bytes)
		}
	}
}
