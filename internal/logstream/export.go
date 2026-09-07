package logstream

import (
	"fmt"
	"io"
	"strings"
	"time"
)

type ExportContext struct {
	Filters []string
	Loss    string
	Raw     bool
}

func exportTransform(context ExportContext) func(string) string {
	if context.Raw {
		return Sanitize
	}
	return SafeText
}
func exportHeader(w io.Writer, context ExportContext) error {
	transform := exportTransform(context)
	_, err := fmt.Fprintf(w, "# k9+ observed logs; filters=%s; loss=%s; raw=%t\n", transform(strings.Join(context.Filters, " AND ")), transform(context.Loss), context.Raw)
	return err
}

//nolint:gocritic // Export transforms a private entry copy when redaction is enabled.
func exportEntry(w io.Writer, entry Entry, context ExportContext, redactor *Redactor) error {
	if !context.Raw {
		entry = redactor.Transform(entry)
	}
	text := fmt.Sprintf(
		"[%s cluster=%s context=%s namespace=%s pod=%s uid=%s container=%s generation=%d id=%d occurrences=%d repeats=%d truncated=%t] %s\n",
		entry.RuntimeTime.Format(time.RFC3339Nano), entry.Source.Cluster, entry.Source.Context, entry.Source.Namespace,
		entry.Source.Pod, entry.Source.UID, entry.Source.Container, entry.Source.Generation, entry.ID, entry.Occurrences,
		entry.Repeats, entry.Truncated, entry.Raw,
	)
	_, err := io.WriteString(w, exportTransform(context)(text))
	return err
}
func Export(w io.Writer, entries []Entry, context ExportContext) error {
	if err := exportHeader(w, context); err != nil {
		return err
	}
	redactor := NewRedactor(1024)
	for i := range entries {
		if err := exportEntry(w, entries[i], context, redactor); err != nil {
			return err
		}
	}
	return nil
}
func Snippet(entries []Entry, context ExportContext) string {
	var b strings.Builder
	_ = Export(&b, entries, context)
	return b.String()
}

// Export streams the complete retained recording in committed ID order. The recorder
// lock freezes segment retention during the scan; writes may wait for export to finish.
func (r *Recorder) Export(w io.Writer, context ExportContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := exportHeader(w, context); err != nil {
		return err
	}
	redactor := NewRedactor(1024)
	for _, sg := range r.segments {
		if err := scanRecords(sg.path, r.opts.MaxRecordBytes, func(e Entry, _, _ int64) error { return exportEntry(w, e, context, redactor) }, nil); err != nil {
			return err
		}
	}
	return nil
}
