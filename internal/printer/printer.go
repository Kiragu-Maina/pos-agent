// Package printer talks ESC/POS to thermal receipt printers.
//
// For v1 this covers network printers on a raw port (typically 9100). USB
// printing is platform specific and lands next; see usb.go stubs. We speak raw
// ESC/POS rather than going through an OS print driver because it gives precise
// control over receipt formatting on cheap 58mm and 80mm printers, which is
// exactly where driver based printing tends to add unwanted margins.
package printer

import (
	"bytes"
	"net"
	"time"
)

// connectTimeout bounds how long we wait to reach a network printer.
const connectTimeout = 3 * time.Second

// ESC/POS control sequences. Kept minimal and well commented so the receipt
// renderer can grow on top of these without re-deriving the protocol.
var (
	escInit     = []byte{0x1B, 0x40}       // ESC @    reset to defaults
	alignCenter = []byte{0x1B, 0x61, 0x01} // ESC a 1  center
	alignLeft   = []byte{0x1B, 0x61, 0x00} // ESC a 0  left
	boldOn      = []byte{0x1B, 0x45, 0x01} // ESC E 1  emphasis on
	boldOff     = []byte{0x1B, 0x45, 0x00} // ESC E 0  emphasis off
	doubleSize  = []byte{0x1D, 0x21, 0x11} // GS ! 0x11 double width+height
	normalSize  = []byte{0x1D, 0x21, 0x00} // GS ! 0    normal size
	feedAndCut  = []byte{0x1D, 0x56, 0x42, 0x00}
	lineFeed    = []byte{0x0A}
)

// SendRaw opens a TCP connection to addr (host:port) and writes payload. This
// is the single network path used by TestPrint and, later, real receipts.
func SendRaw(addr string, payload []byte) error {
	conn, err := net.DialTimeout("tcp", addr, connectTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(connectTimeout))
	_, err = conn.Write(payload)
	return err
}

// TestPrint sends a short, human friendly test receipt to a network printer at
// addr. Seeing this come out of the printer is how a non-technical user
// confirms setup, no status codes involved.
func TestPrint(addr string) error {
	return SendRaw(addr, testReceipt())
}

// testReceipt builds the ESC/POS bytes for the confirmation slip. Front-facing
// text here follows the product rules: plain language, no jargon, no em dashes.
func testReceipt() []byte {
	var b bytes.Buffer
	b.Write(escInit)

	b.Write(alignCenter)
	b.Write(doubleSize)
	b.Write(boldOn)
	b.WriteString("Printer ready\n")
	b.Write(boldOff)
	b.Write(normalSize)

	b.WriteString("\n")
	b.WriteString("Your receipt printer is set up\n")
	b.WriteString("and working.\n")
	b.WriteString("\n")
	b.WriteString("You can start selling.\n")

	b.Write(alignLeft)
	b.Write(lineFeed)
	b.Write(feedAndCut)
	return b.Bytes()
}
