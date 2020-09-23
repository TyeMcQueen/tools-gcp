/*
This package deals with sets of metric labels and lists of label values.
*/
package label

import (
	"bytes"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/golang/protobuf/proto"
	dto "github.com/prometheus/client_model/go"
	"github.com/TyeMcQueen/go-lager"
	"google.golang.org/api/monitoring/v3"
)


// A label.Set tracks the possible label names and seen label values for a
// metric.
type Set struct {
	labelKeys,                  // Label names for a StackDriver metric.
	resourceKeys,               // Label names for a monitored resource.
	keptKeys        []string    // The above 2 lists minus any ignored labels.
	valueSet        Values      // The list of seen label values.
	SkippedKeys     []string    // Sorted list of omitted keys.
}

// A label.Values contains all of the seen label values and provides a mapping
// between each unique value and a rune.  This allows a list of label values
// to be recorded very compactly which also provides an efficient way to find
// the prior metric having identical label values.
type Values struct {
	values  []string
	valPos  map[string]rune
}

// A RuneList is just a string.  It is used to store a list of values of type
// `rune`.
type RuneList string


// Converts a RuneList into a string containing the (decimal) rune values
// separated by "."s.  Makes a RuneList easy to read instead of a string full
// of control characters.  Useful if a RuneList ends up in a log message.
func (rl RuneList) String() string {
	b := new(bytes.Buffer)
	sep := ""
	for _, r := range rl {
		fmt.Fprintf(b, "%s%d", sep, r)
		sep = "."
	}
	return b.String()
}


// Returns 1 plus the count of already-seen unique label values.
func (vs *Values) Len() int {
	return len(vs.values)
}


// Converts a label value into a rune.
func (vs *Values) Rune(val string) rune {
	if p, ok := vs.valPos[val]; ok {
		return p
	}
	p := rune(len(vs.values))
	vs.valPos[val] = p
	vs.values = append(vs.values, val)
	return p
}


// Converts a rune into a label value.  Will panic() if the rune value is
// out-of-range.
func (vs *Values) Value(pos rune) string {
	if pos < 1 || len(vs.values) <= int(pos) {
		lager.Panic().Map(
			"Rune out of range", pos,
			"Value list len", len(vs.values),
		)
	}
	return vs.values[pos]
}


// Returns the number of kept label names.
func (ls *Set) Len() int { return len(ls.keptKeys) }


// Returns the list of kept label names.
func (ls *Set) KeptKeys() []string { return ls.keptKeys }


// Converts a RuneList into a list of LabelPairs ready to export.
func (ls *Set) LabelPairs(rl RuneList) []*dto.LabelPair {
	pairs := make([]*dto.LabelPair, len(ls.keptKeys))
	values := ls.ValueList(rl)
	for i, k := range ls.keptKeys {
		pairs[i] = &dto.LabelPair{
			Name:  proto.String(k),
			Value: proto.String(values[i]),
		}
	}
	return pairs
}


// Initializes a new label.Set to track the passed-in metric labels and
// resource labels, ignoring any labels in `skipKeys`.
func (ls *Set) Init(
	skipKeys        []string,
	labelDescs      []*monitoring.LabelDescriptor,
	resourceLabels  map[string]bool,
) {
	skip := make(map[string]int, len(skipKeys))
	for _, k := range skipKeys {
		skip[k] = 1
	}

	ls.valueSet = Values{
		valPos: make(map[string]rune, 32),
		values: make([]string, 1, 32),
	};
	ls.valueSet.values[0] = "n/a"   // Skip \x00 as a rune for future use.

	ls.labelKeys = make([]string, len(labelDescs))
	skips := 0
	o := 0
	for _, ld := range labelDescs {
		if 0 < skip[ld.Key] {
			if 1 == skip[ld.Key] {
				skips++
			}
			skip[ld.Key]++
		} else {
			ls.labelKeys[o] = ld.Key
			o++
		}
	}
	ls.labelKeys = ls.labelKeys[0:o]
	sort.Strings(ls.labelKeys)

	ls.resourceKeys = make([]string, len(resourceLabels))
	o = 0
	for k, _ := range resourceLabels {
		if 0 < skip[k] {
			if 1 == skip[k] {
				skips++
			}
			skip[k]++
		} else {
			ls.resourceKeys[o] = k
			o++
		}
	}
	ls.resourceKeys = ls.resourceKeys[0:o]
	sort.Strings(ls.resourceKeys)

	if 0 < skips {
		ls.SkippedKeys = make([]string, skips)
		o = 0
		for k, n := range skip {
			if 1 < n {
				ls.SkippedKeys[o] = k
				o++
			}
		}
	}
	sort.Strings(ls.SkippedKeys)

	ls.keptKeys = append(ls.labelKeys, ls.resourceKeys...)
}


// Converts the label values from a TimeSeries into a RuneList.
func (ls *Set) RuneList(
	metricLabels    map[string]string,
	resourceLabels  map[string]string,
) RuneList {
	b := make([]byte, func() int {
		l := len(ls.labelKeys) + len(ls.resourceKeys)
		r := ls.valueSet.Len()
		l *= utf8.RuneLen(rune(r-1+l))
		return l
	}())
	o := 0

	for _, k := range ls.labelKeys {
		val := metricLabels[k]
		r := ls.valueSet.Rune(val)
		o += utf8.EncodeRune(b[o:], r)
	}

	for _, k := range ls.resourceKeys {
		val := resourceLabels[k]
		r := ls.valueSet.Rune(val)
		o += utf8.EncodeRune(b[o:], r)
	}

	return RuneList(string(b[:o]))
}


// Converts a RuneList into a list of just the label value strings.
func (ls *Set) ValueList(rl RuneList) []string {
	l := len(ls.keptKeys)
	vals := make([]string, l)
	i := 0
	for _, r := range rl {
		if len(vals) <= i {
			lager.Panic().Map(
				"RuneList too long",    rl,
				"labelKeys",            len(ls.labelKeys),
				"resourceKeys",         len(ls.resourceKeys),
			)
		}
		vals[i] = ls.valueSet.Value(r)
		i++
	}
	return vals
}
