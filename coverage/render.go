package coverage

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Write prints the human form: a volume table, then optionally the sections
// with gaps, worst first. The caller decides about the gap list, because for all
// six fascicles at once it is several hundred lines nobody reads.
func (r Report) Write(out io.Writer, gaps bool) error {
	// Fixed widths rather than a tabwriter. The counts have to be right aligned to
	// be comparable at a glance and the volume name left aligned to be readable,
	// and a tabwriter aligns a whole table one way or the other.
	headers := []string{"total", "solved", "imported", "published", "verified", "missing"}
	row := func(name string, counts ...int) error {
		line := fmt.Sprintf("%-8s", name)
		for index, count := range counts {
			line += fmt.Sprintf("  %*d", len(headers[index]), count)
		}
		_, err := fmt.Fprintln(out, line)
		return err
	}
	if _, err := fmt.Fprintf(out, "%-8s  %s\n", "volume", strings.Join(headers, "  ")); err != nil {
		return err
	}
	for _, volume := range r.Volumes {
		if err := row(volume.Dir, volume.Total, volume.Solved, volume.Imported,
			volume.Published, volume.Verified, volume.Missing); err != nil {
			return err
		}
	}
	// A single volume needs no total row repeating the one row above it.
	if len(r.Volumes) > 1 {
		if err := row("total", r.Total, r.Solved, r.Imported, r.Published, r.Verified, r.Missing); err != nil {
			return err
		}
	}
	if !gaps {
		return nil
	}
	for _, volume := range r.Volumes {
		rows := volume.Gaps()
		if len(rows) == 0 {
			continue
		}
		if _, err := fmt.Fprintf(out, "\n%s sections with gaps, worst first\n", volume.Dir); err != nil {
			return err
		}
		list := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, section := range rows {
			if _, err := fmt.Fprintf(list, "  %s\t%d missing / %d\n", section.ID, len(section.Missing), section.Total); err != nil {
				return err
			}
		}
		if err := list.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// WriteMissing prints the machine readable queue, one "section number" pair per
// line. This is what the runner consumes.
func (r Report) WriteMissing(out io.Writer) error {
	var builder strings.Builder
	for _, target := range r.Queue() {
		builder.WriteString(target.String())
		builder.WriteByte('\n')
	}
	_, err := io.WriteString(out, builder.String())
	return err
}

// WriteOrphans prints published exercises the source repository does not
// enumerate. It reports and never deletes: throwing away a published proof
// because an enumeration changed would be the wrong instinct.
func (r Report) WriteOrphans(out io.Writer) error {
	count := 0
	for _, volume := range r.Volumes {
		for _, section := range volume.Sections {
			for _, number := range section.Orphans {
				count++
				if _, err := fmt.Fprintf(out, "vol%d/%s/%02d.md is published but %s %d is not in %s\n",
					section.VolumeNumber, section.ID, number, section.ID, number, volume.Dir); err != nil {
					return err
				}
			}
		}
	}
	if count == 0 {
		_, err := fmt.Fprintln(out, "no orphans: every published exercise is enumerated in the source repository")
		return err
	}
	_, err := fmt.Fprintf(out, "%d orphans\n", count)
	return err
}
