package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Latency summarizes one metric's samples. Times are milliseconds, so the
// JSON reads without a unit table; Bytes is the mean reply size.
type Latency struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
	Mean  float64 `json:"mean_ms"`
	Bytes int     `json:"mean_bytes,omitempty"`
}

// Value is a single number that is not a latency distribution (peak RSS,
// a count of cancelled replies).
type Value struct {
	Name  string  `json:"name"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
}

// Env records where the numbers came from; they mean nothing without it.
type Env struct {
	GoVersion string `json:"go_version"`
	CPU       string `json:"cpu"`
	Kernel    string `json:"kernel"`
	Server    string `json:"server"`
	Date      string `json:"date"`
}

// Report is the -json output and the -compare input.
type Report struct {
	Env       Env       `json:"env"`
	Latencies []Latency `json:"latencies"`
	Values    []Value   `json:"values"`
}

// recorder collects samples in first-seen metric order.
type recorder struct {
	order  []string
	series map[string]*series
	values []Value
}

type series struct {
	d     []time.Duration
	bytes int
}

func newRecorder() *recorder {
	return &recorder{series: map[string]*series{}}
}

func (r *recorder) add(name string, d time.Duration, bytes int) {
	s := r.series[name]
	if s == nil {
		s = &series{}
		r.series[name] = s
		r.order = append(r.order, name)
	}
	s.d = append(s.d, d)
	s.bytes += bytes
}

func (r *recorder) value(name, unit string, v float64) {
	r.values = append(r.values, Value{Name: name, Unit: unit, Value: v})
}

// percentile is nearest-rank over sorted samples: p95 of 20 samples is the
// 19th, a sample that was actually observed rather than an interpolation.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	rank = max(rank, 1)
	return sorted[min(rank, len(sorted))-1]
}

func ms(d time.Duration) float64 {
	return math.Round(float64(d)/float64(time.Microsecond)) / 1000
}

func (r *recorder) report(env Env) Report {
	rep := Report{Env: env, Values: r.values}
	for _, name := range r.order {
		s := r.series[name]
		sorted := append([]time.Duration(nil), s.d...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		var sum time.Duration
		for _, d := range sorted {
			sum += d
		}
		rep.Latencies = append(rep.Latencies, Latency{
			Name:  name,
			Count: len(sorted),
			P50:   ms(percentile(sorted, 50)),
			P95:   ms(percentile(sorted, 95)),
			P99:   ms(percentile(sorted, 99)),
			Max:   ms(sorted[len(sorted)-1]),
			Mean:  ms(sum / time.Duration(len(sorted))),
			Bytes: s.bytes / len(sorted),
		})
	}
	if rep.Values == nil {
		rep.Values = []Value{}
	}
	return rep
}

// writeTable prints the human-readable form.
func writeTable(w io.Writer, rep Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "metric\tcount\tp50 ms\tp95 ms\tp99 ms\tmax ms\tmean B\t\n")
	for _, l := range rep.Latencies {
		fmt.Fprintf(tw, "%s\t%d\t%.3f\t%.3f\t%.3f\t%.3f\t%d\t\n", l.Name, l.Count, l.P50, l.P95, l.P99, l.Max, l.Bytes)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	if len(rep.Values) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "value\tunit\tvalue\t\n")
	for _, v := range rep.Values {
		fmt.Fprintf(tw, "%s\t%s\t%g\t\n", v.Name, v.Unit, v.Value)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}

func writeJSON(path string, rep Report) error {
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func readReport(path string) (Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read report: %w", err)
	}
	var rep Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return Report{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return rep, nil
}

// delta formats a change as a percentage; a negative number is faster or
// smaller, which for every metric here is better.
func delta(old, cur float64) string {
	if old == 0 {
		if cur == 0 {
			return "0%"
		}
		return "new"
	}
	return fmt.Sprintf("%+.1f%%", (cur-old)/old*100)
}

// compare prints, for every metric present in both reports, old and new
// p50, p95 and max with the relative change.
func compare(w io.Writer, old, cur Report) error {
	prev := map[string]Latency{}
	for _, l := range old.Latencies {
		prev[l.Name] = l
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "metric\told p50\tnew p50\tdelta\told p95\tnew p95\tdelta\told max\tnew max\tdelta\t\n")
	var missing []string
	for _, l := range cur.Latencies {
		o, ok := prev[l.Name]
		if !ok {
			missing = append(missing, l.Name)
			continue
		}
		fmt.Fprintf(tw, "%s\t%.3f\t%.3f\t%s\t%.3f\t%.3f\t%s\t%.3f\t%.3f\t%s\t\n", l.Name,
			o.P50, l.P50, delta(o.P50, l.P50), o.P95, l.P95, delta(o.P95, l.P95), o.Max, l.Max, delta(o.Max, l.Max))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write comparison: %w", err)
	}
	prevV := map[string]Value{}
	for _, v := range old.Values {
		prevV[v.Name] = v
	}
	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "value\tunit\told\tnew\tdelta\t\n")
	for _, v := range cur.Values {
		o, ok := prevV[v.Name]
		if !ok {
			missing = append(missing, v.Name)
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%g\t%g\t%s\t\n", v.Name, v.Unit, o.Value, v.Value, delta(o.Value, v.Value))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write comparison: %w", err)
	}
	if len(missing) > 0 {
		fmt.Fprintf(w, "\nonly in the new report: %s\n", strings.Join(missing, ", "))
	}
	return nil
}
