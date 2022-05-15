// Misc. helpers for displaying information.
package display

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"google.golang.org/api/monitoring/v3"
)

func Join(sep string, strs ...string) string { return strings.Join(strs, sep) }

func DurationString(d time.Duration) string {
	type u struct {
		suf string
		dur time.Duration
	}
	if d == time.Duration(0) {
		return "0"
	}
	for _, unit := range []u{
		u{"d", 24 * time.Hour},
		u{"h", time.Hour},
		u{"m", time.Minute},
		u{"s", time.Second},
		u{"ms", time.Millisecond},
		u{"us", time.Microsecond},
	} {
		if 0 == d%unit.dur {
			return fmt.Sprintf("%d%s", d/unit.dur, unit.suf)
		} else if 2*unit.dur <= d {
			return fmt.Sprintf("%.2f%s",
				float64(d)/float64(unit.dur), unit.suf)
		}
	}
	return fmt.Sprintf("%dns", d)
}

func DumpJson(indent string, ix interface{}) {
	j := json.NewEncoder(os.Stdout)
	if "" != indent {
		j.SetIndent("", indent)
	}
	err := j.Encode(ix)
	if err != nil {
		fmt.Printf("Unable to marshal to JSON: %v\n", err)
	}
}

func BucketInfo(
	tsValue *monitoring.TypedValue,
) (bucketType string, buckets interface{}) {
	bo := tsValue.DistributionValue.BucketOptions
	if nil != bo.ExplicitBuckets {
		buckets = bo.ExplicitBuckets
	} else if nil != bo.ExponentialBuckets {
		bucketType = "Geometric"
		buckets = bo.ExponentialBuckets
	} else if nil != bo.LinearBuckets {
		bucketType = "Linear"
		buckets = bo.LinearBuckets
	}
	return
}

var wideLine = regexp.MustCompile(`(?m)^[^\n]{1,74}( )[^\n]*`)

// WrapText() returns the passed-in string but with any lines longer than
// 74 bytes wrapped by replacing a space with a newline followed by 5 spaces
// (so they look nice when indented 4 spaces).
func WrapText(line string) string {
	buf := []byte(line)
	left := buf
	for {
		loc := wideLine.FindSubmatchIndex(left)
		if nil == loc {
			break
		}
		if 75 <= loc[1]-loc[0] {
			left[loc[2]] = '\n'
			left = left[loc[2]:]
		} else {
			left = left[loc[1]:]
		}
	}
	return strings.ReplaceAll(string(buf), "\n", "\n     ")
}
