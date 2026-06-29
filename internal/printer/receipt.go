package printer

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// ReceiptData is the neutral input for rendering a sale receipt. The web layer
// maps a stored sale onto this so the printer package stays decoupled from the
// store package.
type ReceiptData struct {
	ShopName   string
	HeaderLine string // optional line under the name (phone, tagline, address)
	Footer     string // ending message, may be multiple lines
	Logo       []byte // optional logo image bytes (png or jpeg), printed at top
	When       time.Time
	WidthMM    int    // 58 or 80
	Theme      string // "classic", "minimal", or "bold"
	Items      []ReceiptItem
	// Tax snapshot. When TaxCents is zero the receipt prints exactly as an
	// untaxed sale (no subtotal or tax line). SubtotalCents is the pre-tax (or,
	// for an inclusive sale, the gross) amount; TaxMode is "inclusive" or
	// "exclusive"; TaxRateBps drives the printed label, e.g. 1600 -> "VAT (16%)".
	SubtotalCents int64
	TaxCents      int64
	TaxRateBps    int
	TaxMode       string
	TotalCents    int64
	PaidCents     int64
	ChangeCents   int64
}

// ReceiptItem is one printed line.
type ReceiptItem struct {
	Name       string
	Qty        int
	PriceCents int64 // unit price
}

// line is one rendered receipt line in a theme-neutral form. The same lines
// drive both the ESC/POS bytes and the on-screen preview, so the two never
// drift. Double size is only ever used on short centered text (the shop name),
// where it cannot break column alignment.
type line struct {
	center bool
	bold   bool
	double bool
	text   string
}

// charsForWidth maps paper width in millimetres to the printable character
// count for a standard Font A receipt.
func charsForWidth(mm int) int {
	if mm == 58 {
		return 32
	}
	return 48 // 80mm default
}

// PrintReceipt renders a sale and sends it to the network printer at addr.
func PrintReceipt(addr string, d ReceiptData) error {
	return SendRaw(addr, RenderReceipt(d))
}

// RenderReceipt builds the ESC/POS bytes for a sale receipt in the chosen theme.
// When a logo is present it is printed, centered, at the very top.
func RenderReceipt(d ReceiptData) []byte {
	w := charsForWidth(d.WidthMM)
	var b bytes.Buffer
	b.Write(escInit)

	if len(d.Logo) > 0 {
		if raster, err := rasterFromImage(d.Logo, paperDots(d.WidthMM)); err == nil {
			b.Write(alignCenter)
			b.Write(raster)
			b.WriteString("\n")
			b.Write(alignLeft)
		}
	}

	writeLines(&b, themeLines(d, w))

	b.Write(alignLeft)
	b.Write(lineFeed)
	b.Write(feedAndCut)
	return b.Bytes()
}

// PreviewText returns the plain-text layout of a receipt for on-screen preview.
// It mirrors what RenderReceipt prints, minus the ESC/POS control codes.
func PreviewText(d ReceiptData) string {
	w := charsForWidth(d.WidthMM)
	return linesToText(themeLines(d, w), w)
}

// themeLines dispatches to the chosen theme's line builder.
func themeLines(d ReceiptData, w int) []line {
	switch d.Theme {
	case "minimal":
		return minimalLines(d, w)
	case "bold":
		return boldLines(d, w)
	default:
		return classicLines(d, w)
	}
}

// --- Themes ---

// Classic: centered double-size name, hyphen rules, bold total, warm footer.
func classicLines(d ReceiptData, w int) []line {
	var ls []line
	ls = append(ls,
		line{center: true, bold: true, double: true, text: d.ShopName},
		line{center: true, text: d.When.Format("02 Jan 2006  3:04 PM")},
	)
	if h := strings.TrimSpace(d.HeaderLine); h != "" {
		ls = append(ls, line{center: true, text: h})
	}
	ls = append(ls, line{text: repeat("-", w)})
	ls = append(ls, itemLines(d, w)...)
	ls = append(ls, line{text: repeat("-", w)})
	ls = append(ls, moneyLines(d, w, "TOTAL", true)...)
	for _, f := range footerLines(d.Footer) {
		ls = append(ls, line{center: true, text: f})
	}
	return ls
}

// Minimal: quiet, left aligned, blank-line spacing, no rules.
func minimalLines(d ReceiptData, w int) []line {
	var ls []line
	ls = append(ls,
		line{bold: true, text: d.ShopName},
		line{text: d.When.Format("02 Jan 2006  3:04 PM")},
	)
	if h := strings.TrimSpace(d.HeaderLine); h != "" {
		ls = append(ls, line{text: h})
	}
	ls = append(ls, line{text: ""})
	ls = append(ls, itemLines(d, w)...)
	ls = append(ls, line{text: ""})
	ls = append(ls, moneyLines(d, w, "Total", false)...)
	for _, f := range footerLines(d.Footer) {
		ls = append(ls, line{text: f})
	}
	return ls
}

// Bold: heavy = rules, big centered name, strong footer.
func boldLines(d ReceiptData, w int) []line {
	var ls []line
	ls = append(ls,
		line{text: repeat("=", w)},
		line{center: true, bold: true, double: true, text: d.ShopName},
		line{center: true, text: d.When.Format("02 Jan 2006  3:04 PM")},
	)
	if h := strings.TrimSpace(d.HeaderLine); h != "" {
		ls = append(ls, line{center: true, text: h})
	}
	ls = append(ls, line{text: repeat("=", w)})
	ls = append(ls, itemLines(d, w)...)
	ls = append(ls, line{text: repeat("=", w)})
	ls = append(ls, moneyLines(d, w, "TOTAL", true)...)
	for _, f := range footerLines(d.Footer) {
		ls = append(ls, line{center: true, bold: true, text: f})
	}
	return ls
}

// moneyLines builds the totals block: an optional subtotal and tax line, the
// TOTAL, an optional inclusive-tax note, then cash and change. When TaxCents is
// zero it collapses to exactly the old TOTAL/Cash/Change block, so an untaxed
// receipt is unchanged. totalLabel and totalBold let each theme keep its style.
func moneyLines(d ReceiptData, w int, totalLabel string, totalBold bool) []line {
	var ls []line
	inclusive := d.TaxMode == "inclusive"
	if d.TaxCents > 0 && !inclusive {
		ls = append(ls,
			line{text: leftRight("Subtotal", money(d.SubtotalCents), w)},
			line{text: leftRight(vatLabel(d.TaxRateBps), money(d.TaxCents), w)},
		)
	}
	ls = append(ls, line{bold: totalBold, text: leftRight(totalLabel, money(d.TotalCents), w)})
	if d.TaxCents > 0 && inclusive {
		ls = append(ls, line{text: leftRight("Incl. "+vatLabel(d.TaxRateBps), money(d.TaxCents), w)})
	}
	ls = append(ls,
		line{text: leftRight("Cash", money(d.PaidCents), w)},
		line{text: leftRight("Change", money(d.ChangeCents), w)},
	)
	return ls
}

// vatLabel renders a tax rate in basis points as a receipt label, trimming a
// trailing ".0" so 1600 -> "VAT (16%)" and 1650 -> "VAT (16.5%)".
func vatLabel(rateBps int) string {
	whole := rateBps / 100
	frac := rateBps % 100
	if frac == 0 {
		return fmt.Sprintf("VAT (%d%%)", whole)
	}
	s := fmt.Sprintf("%d.%02d", whole, frac)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return fmt.Sprintf("VAT (%s%%)", s)
}

// footerLines splits the editable footer into printable lines. A leading blank
// line separates the totals from the message. An empty footer prints nothing.
func footerLines(footer string) []string {
	if strings.TrimSpace(footer) == "" {
		return nil
	}
	out := []string{""}
	for _, l := range strings.Split(footer, "\n") {
		out = append(out, strings.TrimRight(l, "\r"))
	}
	return out
}

// itemLines formats each sale item as a full-width name/amount row.
func itemLines(d ReceiptData, w int) []line {
	ls := make([]line, 0, len(d.Items))
	for _, it := range d.Items {
		name := it.Name
		if it.Qty > 1 {
			name = fmt.Sprintf("%s x%d", it.Name, it.Qty)
		}
		ls = append(ls, line{text: leftRight(name, money(it.PriceCents*int64(it.Qty)), w)})
	}
	return ls
}

// --- Renderers ---

// writeLines emits the line content (alignment, emphasis, size) into b. The
// caller frames it with init and cut, and may prepend a logo.
func writeLines(b *bytes.Buffer, ls []line) {
	for _, ln := range ls {
		if ln.center {
			b.Write(alignCenter)
		} else {
			b.Write(alignLeft)
		}
		if ln.double {
			b.Write(doubleSize)
		}
		if ln.bold {
			b.Write(boldOn)
		}
		b.WriteString(ln.text)
		b.WriteString("\n")
		if ln.bold {
			b.Write(boldOff)
		}
		if ln.double {
			b.Write(normalSize)
		}
	}
}

func linesToText(ls []line, w int) string {
	var sb strings.Builder
	for _, ln := range ls {
		t := ln.text
		if ln.center {
			t = center(t, w)
		}
		sb.WriteString(t)
		sb.WriteString("\n")
	}
	return sb.String()
}

// --- Text helpers ---

// leftRight formats a line with left text and right text padded to width. If
// the two would not fit, the left text is truncated so the amount stays intact.
func leftRight(left, right string, width int) string {
	space := width - len(right)
	if space < 1 {
		space = 1
	}
	if len(left) > space-1 {
		if space-1 < 0 {
			left = ""
		} else {
			left = left[:space-1]
		}
	}
	pad := width - len(left) - len(right)
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// center pads text with leading spaces so it sits centered within width.
func center(text string, width int) string {
	if len(text) >= width {
		return text
	}
	pad := (width - len(text)) / 2
	return strings.Repeat(" ", pad) + text
}

func repeat(ch string, width int) string {
	return strings.Repeat(ch, width)
}

// money formats integer cents as a Kenyan shilling amount, for example
// "KSh 1,234.50".
func money(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	shillings := cents / 100
	rem := cents % 100
	out := fmt.Sprintf("KSh %s.%02d", groupThousands(shillings), rem)
	if neg {
		out = "-" + out
	}
	return out
}

// groupThousands inserts commas into a non-negative integer.
func groupThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
