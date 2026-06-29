package web

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"
)

// writePDF renders the table as a simple, valid PDF for printing or filing,
// written with only the standard library so the local agent stays pure-Go and
// Windows 7 capable. The layout is a monospaced (Courier) table with a bold
// heading; money columns are right-aligned. Long rows of records flow across as
// many pages as needed, each repeating the column headings. No font is embedded:
// Courier and Helvetica-Bold are two of the 14 standard PDF fonts every reader
// already has.
func writePDF(w io.Writer, t exportTable) error {
	const (
		pageW, pageH = 612.0, 792.0 // US Letter, points
		left         = 40.0
		titleY       = 752.0
		titleSize    = 14.0
		bodySize     = 8.0
		lineH        = 12.0
		bottom       = 48.0
	)
	// Courier advance width is 0.6em; this many characters fit the text column.
	textWidth := (pageW - 2*left) / (bodySize * 0.6)
	maxChars := int(textWidth)

	widths := fitWidths(t, maxChars)
	header := formatRow(t.headers, widths, nil) // headings left-aligned
	sep := strings.Repeat("-", lineLen(widths))

	var dataLines []string
	if len(t.rows) == 0 {
		dataLines = []string{"(no records in this range)"}
	} else {
		for _, row := range t.rows {
			dataLines = append(dataLines, formatRow(row, widths, t.numeric))
		}
	}

	// How many data lines fit below the title + header + separator on one page.
	usable := titleY - 22 - 2*lineH - bottom
	perPage := int(usable/lineH) + 1
	if perPage < 1 {
		perPage = 1
	}

	stamp := time.Now().Format("2006-01-02")
	titleText := fmt.Sprintf("%s   %s   (%d)", t.title, stamp, len(t.rows))

	// Build one content stream per page.
	var streams []string
	for start := 0; start < len(dataLines); start += perPage {
		end := start + perPage
		if end > len(dataLines) {
			end = len(dataLines)
		}
		var c bytes.Buffer
		c.WriteString("BT\n")
		fmt.Fprintf(&c, "/F2 %g Tf\n", titleSize)
		fmt.Fprintf(&c, "%g %g Td\n", left, titleY)
		fmt.Fprintf(&c, "(%s) Tj\n", pdfString(titleText))
		fmt.Fprintf(&c, "/F1 %g Tf\n", bodySize)
		fmt.Fprintf(&c, "0 -22 Td\n(%s) Tj\n", pdfString(header))
		fmt.Fprintf(&c, "0 -%g Td\n(%s) Tj\n", lineH, pdfString(sep))
		for _, line := range dataLines[start:end] {
			fmt.Fprintf(&c, "0 -%g Td\n(%s) Tj\n", lineH, pdfString(line))
		}
		c.WriteString("ET\n")
		streams = append(streams, c.String())
	}
	if len(streams) == 0 { // empty table still produces one page
		streams = append(streams, "BT\n/F2 14 Tf\n40 752 Td\n("+pdfString(titleText)+") Tj\nET\n")
	}

	return assemblePDF(w, streams, pageW, pageH)
}

// assemblePDF writes the object graph: catalog, page tree, the two standard
// fonts, then a page and a content stream per rendered page. It tracks each
// object's byte offset for the cross-reference table the trailer points at.
func assemblePDF(w io.Writer, streams []string, pageW, pageH float64) error {
	n := len(streams)
	// Object numbering: 1 catalog, 2 pages, 3 Courier, 4 Helvetica-Bold,
	// then per page i: content = 5+2i, page = 6+2i.
	contentObj := func(i int) int { return 5 + 2*i }
	pageObj := func(i int) int { return 6 + 2*i }

	objects := make([]string, 0, 4+2*n)

	objects = append(objects, "<< /Type /Catalog /Pages 2 0 R >>")

	var kids strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&kids, "%d 0 R ", pageObj(i))
	}
	objects = append(objects, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.TrimSpace(kids.String()), n))

	objects = append(objects, "<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>")
	objects = append(objects, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>")

	for i := 0; i < n; i++ {
		s := streams[i]
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s))
		objects = append(objects, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g] "+
				"/Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			pageW, pageH, contentObj(i)))
	}

	// Emit the file, recording byte offsets of each object for the xref table.
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xrefAt := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xrefAt)

	_, err := w.Write(buf.Bytes())
	return err
}

// fitWidths computes a character width for each column: the wider of the heading
// and the widest cell, capped, then shrinks the widest columns until the row fits
// the page. Money columns are kept intact (never truncated).
func fitWidths(t exportTable, budget int) []int {
	const cap = 44
	widths := make([]int, len(t.headers))
	for c, h := range t.headers {
		widths[c] = len(h)
	}
	for _, row := range t.rows {
		for c, cell := range row {
			if c < len(widths) && len(cell) > widths[c] {
				widths[c] = len(cell)
			}
		}
	}
	for c := range widths {
		if widths[c] > cap {
			widths[c] = cap
		}
	}
	// Shrink the widest shrinkable column until the line fits the page budget.
	for lineLen(widths) > budget {
		victim, best := -1, 0
		for c := range widths {
			if c < len(t.numeric) && t.numeric[c] {
				continue // don't truncate money
			}
			if widths[c] > best {
				best, victim = widths[c], c
			}
		}
		if victim == -1 || widths[victim] <= 6 {
			break // nothing left to give
		}
		widths[victim]--
	}
	return widths
}

// lineLen is the rendered width of a row: every column plus one space between.
func lineLen(widths []int) int {
	total := 0
	for _, w := range widths {
		total += w
	}
	if len(widths) > 1 {
		total += len(widths) - 1
	}
	return total
}

// formatRow lays cells into fixed-width columns separated by a space. numeric[c]
// right-aligns a column; a nil numeric left-aligns everything (used for headings).
func formatRow(cells []string, widths []int, numeric []bool) string {
	parts := make([]string, len(widths))
	for c := range widths {
		cell := ""
		if c < len(cells) {
			cell = cells[c]
		}
		right := numeric != nil && c < len(numeric) && numeric[c]
		parts[c] = cellFit(cell, widths[c], right)
	}
	return strings.Join(parts, " ")
}

// cellFit pads cell to width w, or truncates it with a ".." marker when it is too
// long. right requests right-alignment (for money).
func cellFit(s string, w int, right bool) string {
	if len(s) > w {
		if w <= 2 {
			s = s[:w]
		} else {
			s = s[:w-2] + ".."
		}
	}
	pad := strings.Repeat(" ", w-len(s))
	if right {
		return pad + s
	}
	return s + pad
}

// pdfString escapes a string for a PDF literal: backslash and parentheses are
// escaped, and any control character (which would corrupt the stream) becomes a
// space.
func pdfString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '\\':
			b.WriteString(`\\`)
		case '(':
			b.WriteString(`\(`)
		case ')':
			b.WriteString(`\)`)
		default:
			if ch < 0x20 {
				b.WriteByte(' ')
			} else {
				b.WriteByte(ch)
			}
		}
	}
	return b.String()
}
