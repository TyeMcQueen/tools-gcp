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
	u.Is(17, len(bucketBounds), "exp bucket bounds")
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
			u.Circa(9, want, bucketBounds[i], u.S("exp bucket bound ", i))
		}
	}
	u.Is(17, len(subBuckets), "sub bucket counts")
	for i, want := range []int64{
		8, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, // 27,
	} {
		if i < len(bucketBounds) {
			u.Is(want, subBuckets[i], u.S("exp subbuckets ", i))
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
	u.Circa(13, 14141207.3025, sum, "exp bucket sum")
	u.Is(67, h.SampleCount, "exp bucket hits")

	u.Is(18, len(h.BucketHits), "exp bucket hit len")
	for i, want := range []uint64{
		8, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 27,
	} {
		if i < len(bucketBounds) {
			u.Is(want, h.BucketHits[i], u.S("exp bucket hits ", i))
		}
	}
}
