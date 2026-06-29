package web

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"pos-system/internal/store"
)

// getRaw performs a GET and returns the raw response, asserting a 200.
func getRaw(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (%s)", path, rec.Code, rec.Body.String())
	}
	return rec
}

// readZipFile returns the named entry's bytes from a zip archive, or fails.
func readZipFile(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open %s: %v", name, err)
			}
			defer rc.Close()
			b, _ := io.ReadAll(rc)
			return b
		}
	}
	t.Fatalf("zip is missing %s; has %v", name, zipNames(data))
	return nil
}

func zipNames(data []byte) []string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func TestSalesExportXLSX(t *testing.T) {
	srv, db, _ := newTestServer(t)
	seedSale(t, db, "Bread", 6500, store.PaymentMpesa, "QGH7ABC")
	seedSale(t, db, "Milk", 6000, store.PaymentCash, "")

	rec := getRaw(t, srv, "/api/sales/export?format=xlsx")
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "spreadsheetml.sheet") {
		t.Errorf("Content-Type = %q, want an xlsx type", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".xlsx") || !strings.Contains(cd, "transactions") {
		t.Errorf("Content-Disposition = %q, want a transactions .xlsx", cd)
	}

	body := rec.Body.Bytes()
	// Every part the spreadsheet format requires must be present and well-formed.
	for _, part := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/worksheets/sheet1.xml"} {
		raw := readZipFile(t, body, part)
		if err := xml.Unmarshal(raw, new(struct {
			XMLName xml.Name
		})); err != nil {
			t.Errorf("%s is not well-formed XML: %v", part, err)
		}
	}

	sheet := string(readZipFile(t, body, "xl/worksheets/sheet1.xml"))
	if !strings.Contains(sheet, "Bread x1") {
		t.Errorf("sheet missing the Bread line: %s", sheet)
	}
	// 6500 cents must be a real number cell (<v>65.00</v>), not a string, so totals
	// can be summed in the spreadsheet.
	if !strings.Contains(sheet, "<v>65.00</v>") {
		t.Errorf("sheet missing numeric total 65.00: %s", sheet)
	}
	// Headers are present as inline strings.
	if !strings.Contains(sheet, "Reference") || !strings.Contains(sheet, "inlineStr") {
		t.Errorf("sheet missing headers/inline strings: %s", sheet)
	}
}

func TestAuditExportXLSXEscapesData(t *testing.T) {
	srv, db, _ := newTestServer(t)
	// A name with XML metacharacters must be escaped, not corrupt the workbook.
	if _, err := db.AddProduct(store.ProductDraft{Name: `Tom & <Jerry>`, PriceCents: 5000}); err != nil {
		t.Fatalf("add product: %v", err)
	}
	rec := getRaw(t, srv, "/api/audit/export?format=xlsx")
	body := rec.Body.Bytes()
	sheet := readZipFile(t, body, "xl/worksheets/sheet1.xml")
	if err := xml.Unmarshal(sheet, new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("sheet with special characters is not well-formed: %v", err)
	}
	if !strings.Contains(string(sheet), "Tom &amp; &lt;Jerry&gt;") {
		t.Errorf("special characters not escaped: %s", sheet)
	}
}

func TestSalesExportPDF(t *testing.T) {
	srv, db, _ := newTestServer(t)
	seedSale(t, db, "Bread", 6500, store.PaymentMpesa, "QGH7ABC")

	rec := getRaw(t, srv, "/api/sales/export?format=pdf")
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".pdf") || !strings.Contains(cd, "transactions") {
		t.Errorf("Content-Disposition = %q, want a transactions .pdf", cd)
	}

	body := rec.Body.String()
	if !strings.HasPrefix(body, "%PDF-1.") {
		t.Errorf("body does not start with a PDF header: %.16q", body)
	}
	if !strings.Contains(body, "startxref") || !strings.HasSuffix(strings.TrimSpace(body), "%%EOF") {
		t.Errorf("PDF is missing its trailer/EOF")
	}
	// The heading and a data value should appear in a content stream.
	if !strings.Contains(body, "Transactions") {
		t.Errorf("PDF missing the heading")
	}
	if !strings.Contains(body, "Bread x1") {
		t.Errorf("PDF missing the Bread line")
	}
	// The cross-reference count must match the object count, or readers reject it.
	assertPDFXrefConsistent(t, rec.Body.Bytes())
}

func TestAuditExportPDFEmptyRange(t *testing.T) {
	srv, _, _ := newTestServer(t)
	// No activity at all: the PDF must still be valid, with a friendly empty note.
	rec := getRaw(t, srv, "/api/audit/export?format=pdf&from=2026-06-28&to=2026-06-28")
	body := rec.Body.String()
	if !strings.HasPrefix(body, "%PDF-1.") {
		t.Fatalf("empty-range PDF is not a PDF")
	}
	if !strings.Contains(body, "no records") {
		t.Errorf("empty PDF should say there are no records")
	}
	assertPDFXrefConsistent(t, rec.Body.Bytes())
}

// assertPDFXrefConsistent checks that the startxref offset points at the literal
// "xref" keyword — the spot every reader seeks to first.
func assertPDFXrefConsistent(t *testing.T, body []byte) {
	t.Helper()
	i := bytes.LastIndex(body, []byte("startxref"))
	if i < 0 {
		t.Fatal("no startxref")
	}
	fields := strings.Fields(string(body[i+len("startxref"):]))
	if len(fields) == 0 {
		t.Fatal("startxref has no offset")
	}
	off, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("bad startxref offset %q: %v", fields[0], err)
	}
	if off < 0 || off >= len(body) || !bytes.HasPrefix(body[off:], []byte("xref")) {
		t.Fatalf("startxref %d does not point at an xref table", off)
	}
}

func TestExportHelpers(t *testing.T) {
	// colName
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB"} {
		if got := colName(i); got != want {
			t.Errorf("colName(%d) = %q, want %q", i, got, want)
		}
	}
	// isNumeric
	for s, want := range map[string]bool{"65.00": true, "-12.00": true, "0": true, "": false, "-": false, "1.2.3": false, "12a": false, "M-Pesa": false} {
		if got := isNumeric(s); got != want {
			t.Errorf("isNumeric(%q) = %v, want %v", s, got, want)
		}
	}
	// cellFit: pad and truncate
	if got := cellFit("ab", 5, false); got != "ab   " {
		t.Errorf("cellFit pad-left = %q", got)
	}
	if got := cellFit("65.00", 8, true); got != "   65.00" {
		t.Errorf("cellFit pad-right = %q", got)
	}
	if got := cellFit("abcdefgh", 5, false); got != "abc.." {
		t.Errorf("cellFit truncate = %q", got)
	}
	// sheetTab sanitises and caps length
	if got := sheetTab("a/b:c"); got != "a-b-c" {
		t.Errorf("sheetTab = %q", got)
	}
	if got := sheetTab(strings.Repeat("x", 40)); len(got) != 31 {
		t.Errorf("sheetTab len = %d, want 31", len(got))
	}
}
