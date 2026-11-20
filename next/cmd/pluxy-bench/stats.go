package main

import (
	"fmt"
	"math"
	"slices"
)

// MetricSummary holds the median and min..max range of a metric across runs.
type MetricSummary struct {
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// Summarize computes median, min, and max over vals. Returns zero values if vals is empty.
func Summarize(vals []float64) MetricSummary {
	if len(vals) == 0 {
		return MetricSummary{}
	}
	s := slices.Clone(vals)
	slices.Sort(s)
	n := len(s)
	var med float64
	if n%2 == 1 {
		med = s[n/2]
	} else {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	return MetricSummary{
		Median: med,
		Min:    s[0],
		Max:    s[n-1],
	}
}

// Percentile returns the q-th percentile (q in [0, 1]) from an unsorted slice using nearest-rank.
func Percentile(vals []float64, q float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := slices.Clone(vals)
	slices.Sort(s)
	return PercentileSorted(s, q)
}

// PercentileSorted returns the q-th percentile (q in [0, 1]) from a sorted ascending slice.
func PercentileSorted(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// FormatSummary formats a MetricSummary as "median (min..max)" with the given precision.
func FormatSummary(m MetricSummary, decimals int) string {
	format := fmt.Sprintf("%%.%df (%%.%df..%%.%df)", decimals, decimals, decimals)
	return fmt.Sprintf(format, m.Median, m.Min, m.Max)
}
