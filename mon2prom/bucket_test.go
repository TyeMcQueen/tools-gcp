package mon2prom

import (
	"testing"

	"github.com/TyeMcQueen/go-tutl"
	"github.com/TyeMcQueen/tools-gcp/mon2prom/value"
	sd "google.golang.org/api/monitoring/v3" // "StackDriver"
)

func TestCombineBoundaries(t *testing.T) {
	u := tutl.New(t)

	scaler := func(f float64) float64 { return f / 1000.0 }

	// Exponential Buckets //

	bounds := &sd.BucketOptions{ExponentialBuckets: &sd.Exponential{
		NumFiniteBuckets: 66,
		Scale:            1.0,
		GrowthFactor:     1.4,
	}}
	boundCount, firstBound, nextBound := parseBucketOptions("", bounds, scaler)
	bucketBounds, subBuckets := combineBucketBoundaries(
		boundCount, firstBound, nextBound,
		24, 0.01, 1.9, 600.0,
	)

	u.Is(17, len(bucketBounds), "pow bucket bounds")
	for i, want := range []float64{
		0.0105413504,
		0.020661046784,
		0.04049565169664,
		0.0793714773254143,
		0.155568095557812,
		0.304913467293312,
		0.597630395894891,
		1.17135557595399,
		2.29585692886981,
		4.49987958058483,
		8.81976397794627,
		17.2867373967747,
		33.8820052976784,
		66.4087303834496,
		130.161111551561,
		255.11577864106,
		500.026926136477,
	} {
		if i < len(bucketBounds) {
			u.Circa(9, want, bucketBounds[i], u.S("pow bucket bound ", i))
		}
	}

	u.Is(17, len(subBuckets), "pow sub bucket counts")
	for i, want := range []int64{
		8, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 27,
	} {
		if i < len(bucketBounds) {
			u.Is(want, subBuckets[i], u.S("pow subbuckets ", i))
		}
	}

	h := new(value.RwHistogram)
	sum := h.Convert(subBuckets, &sd.Distribution{
		BucketCounts: []int64{
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0},
		BucketOptions: bounds,
		Count:         67,
		Mean:          211062.795559702,
	})
	u.Circa(13, 14141207.3025, sum, "pow bucket sum")
	u.Is(67, h.SampleCount, "pow bucket hits")

	u.Is(18, len(h.BucketHits), "pow bucket hit len")
	for i, want := range []uint64{
		8, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 27,
	} {
		if i < len(bucketBounds) {
			u.Is(want, h.BucketHits[i], u.S("pow bucket hits ", i))
		}
	}

	// Explicit Buckets //

	bounds = &sd.BucketOptions{ExplicitBuckets: &sd.Explicit{
		Bounds: []float64{
			0, 1, 2, 3, 4, 5, 6, 8, 10, 13, 16, 20, 25, 30, 40, 50, 65, 80,
			100, 130, 160, 200, 250, 300, 400, 500, 650, 800,
			1000, 2000, 5000, 10000, 20000, 50000, 100000,
		},
	}}
	boundCount, firstBound, nextBound = parseBucketOptions("", bounds, scaler)
	bucketBounds, subBuckets = combineBucketBoundaries(
		boundCount, firstBound, nextBound,
		24, 0.005, 1.45, 600.0,
	)

	u.Is(18, len(bucketBounds), "spec bucket bounds")
	for i, want := range []float64{
		0.005, 0.008, 0.013, 0.02, 0.03, 0.05, 0.08, 0.13, 0.2, 0.3, 0.5, 0.8,
		2, 5, 10, 20, 50, 100,
	} {
		if i < len(bucketBounds) {
			u.Circa(9, want, bucketBounds[i], u.S("spec bucket bound ", i))
		}
	}
	u.Is(18, len(subBuckets), "spec sub bucket counts")
	for i, want := range []int64{
		6, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1,
	} {
		if i < len(bucketBounds) {
			u.Is(want, subBuckets[i], u.S("spec subbuckets ", i))
		}
	}

	h = new(value.RwHistogram)
	sum = h.Convert(subBuckets, &sd.Distribution{
		BucketCounts: []int64{
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0,
			1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0, 1.0},
		BucketOptions: bounds,
		Count:         35,
		Mean:          191868.0 / 35.0,
	})
	u.Circa(12, 191868, sum, "spec bucket sum")
	u.Is(35, h.SampleCount, "spec bucket hits")

	u.Is(19, len(h.BucketHits), "spec bucket hit len")
	for i, want := range []uint64{
		6, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1,
	} {
		if i < len(bucketBounds) {
			u.Is(want, h.BucketHits[i], u.S("spec bucket hits ", i))
		}
	}
}
