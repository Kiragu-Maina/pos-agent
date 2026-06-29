package web

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"
)

// writeXLSX renders the table as a minimal but fully valid .xlsx workbook (an
// Office Open XML SpreadsheetML package), built with nothing but the standard
// library's zip writer. A spreadsheet is a ZIP of a few small XML parts; we emit
// exactly the parts Excel, LibreOffice, and Google Sheets need and no more.
// Money columns are written as real numbers so totals can be summed; every other
// cell is an inline string. Keeping this dependency-free preserves the local
// agent's pure-Go, Windows 7 build.
func writeXLSX(w io.Writer, t exportTable) error {
	zw := zip.NewWriter(w)

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{"xl/workbook.xml", workbookXML(t.title)},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML},
		{"xl/worksheets/sheet1.xml", sheetXML(t)},
	}
	for _, p := range parts {
		f, err := zw.Create(p.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, p.body); err != nil {
			return err
		}
	}
	return zw.Close()
}

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
	`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
	`</Types>`

const rootRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

const workbookRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
	`</Relationships>`

// workbookXML names the single sheet. Excel limits a sheet name to 31 characters
// and forbids a few punctuation marks; sanitise so an awkward title never yields a
// corrupt workbook.
func workbookXML(sheetName string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<sheets><sheet name="` + xmlEscape(sheetTab(sheetName)) + `" sheetId="1" r:id="rId1"/></sheets>` +
		`</workbook>`
}

// sheetXML renders the header row and data rows into a worksheet part.
func sheetXML(t exportTable) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)

	// Header row (row 1), all inline strings.
	b.WriteString(`<row r="1">`)
	for c, h := range t.headers {
		writeInlineCell(&b, colName(c)+"1", h)
	}
	b.WriteString(`</row>`)

	for i, row := range t.rows {
		rn := i + 2 // data starts on row 2
		fmt.Fprintf(&b, `<row r="%d">`, rn)
		for c, cell := range row {
			ref := fmt.Sprintf("%s%d", colName(c), rn)
			if c < len(t.numeric) && t.numeric[c] {
				writeNumberCell(&b, ref, cell)
			} else {
				writeInlineCell(&b, ref, cell)
			}
		}
		b.WriteString(`</row>`)
	}

	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

// writeInlineCell writes a string cell carrying its text inline (no shared-strings
// table needed). xml:space="preserve" keeps any leading or trailing spaces.
func writeInlineCell(b *strings.Builder, ref, text string) {
	b.WriteString(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">`)
	b.WriteString(xmlEscape(text))
	b.WriteString(`</t></is></c>`)
}

// writeNumberCell writes a numeric cell. The table's money strings ("65.00",
// "-12.00") are already valid spreadsheet numbers; an unparseable value falls back
// to an inline string so the export never produces a corrupt number.
func writeNumberCell(b *strings.Builder, ref, value string) {
	if !isNumeric(value) {
		writeInlineCell(b, ref, value)
		return
	}
	b.WriteString(`<c r="` + ref + `"><v>` + value + `</v></c>`)
}

// isNumeric reports whether s is a plain decimal number (optional leading minus,
// digits, at most one dot) — the shape decimalCents produces.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	i, dot := 0, false
	if s[0] == '-' {
		if len(s) == 1 {
			return false
		}
		i = 1
	}
	digits := false
	for ; i < len(s); i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
			digits = true
		case s[i] == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return digits
}

// colName turns a zero-based column index into a spreadsheet column letter
// (0->A, 25->Z, 26->AA).
func colName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

// sheetTab sanitises a worksheet tab name to Excel's rules: at most 31 characters
// and none of \ / ? * [ ] :.
func sheetTab(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', '?', '*', '[', ']', ':':
			return '-'
		}
		return r
	}, s)
	if len(s) > 31 {
		s = s[:31]
	}
	if s == "" {
		s = "Sheet1"
	}
	return s
}

// xmlEscape escapes the five XML metacharacters and strips control characters
// that are illegal in XML 1.0 text, so any item name or note is safe to embed.
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		case '\t', '\n', '\r':
			b.WriteRune(r)
		default:
			if r < 0x20 {
				continue // illegal in XML 1.0
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
