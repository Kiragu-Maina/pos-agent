// Command fakeprinter emulates a network thermal printer for testing. It
// listens on a raw TCP port (9100 by default, the ESC/POS RAW port that real
// receipt printers use) and logs every print job it receives as a readable
// summary, a hex dump, and a plain text preview.
//
// It lets you exercise the POS agent's whole print path with no hardware, for
// example as a second service in docker compose. The agent prints to it exactly
// as it would to a real printer.
//
//	PRINTER_ADDR  listen address (default :9100)
package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

const dumpCap = 1024 // cap the hex dump so a big logo does not flood the log

func main() {
	addr := envOr("PRINTER_ADDR", ":9100")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("fakeprinter: cannot bind %s: %v", addr, err)
	}
	log.Printf("fakeprinter: listening on %s, waiting for print jobs", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("fakeprinter: accept failed: %v", err)
			continue
		}
		go handle(conn)
	}
}

func handle(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()

	// A job ends when the agent closes its side. The deadline is only a safety
	// net so a half open connection cannot block this goroutine forever.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	data, err := io.ReadAll(bufio.NewReader(conn))
	if err != nil && !isTimeout(err) {
		log.Printf("fakeprinter: read from %s failed: %v", remote, err)
	}
	if len(data) == 0 {
		log.Printf("fakeprinter: empty connection from %s (likely a discovery probe)", remote)
		return
	}

	log.Printf("fakeprinter: print job from %s, %d bytes [%s]", remote, len(data), features(data))

	dump := data
	if len(dump) > dumpCap {
		dump = dump[:dumpCap]
	}
	log.Printf("fakeprinter: hex dump:\n%s", hex.Dump(dump))
	if len(data) > dumpCap {
		log.Printf("fakeprinter: ... (%d more bytes not shown)", len(data)-dumpCap)
	}
	log.Printf("fakeprinter: receipt text:\n%s", textPreview(data))
}

// features names the ESC/POS control sequences present in a job so the log
// reads as a quick sanity check of what the agent sent.
func features(data []byte) string {
	var f []string
	if bytes.HasPrefix(data, []byte{0x1b, 0x40}) {
		f = append(f, "init")
	}
	if bytes.Contains(data, []byte{0x1d, 0x76, 0x30}) {
		f = append(f, "logo raster")
	}
	if bytes.Contains(data, []byte{0x1b, 0x61}) {
		f = append(f, "alignment")
	}
	if bytes.Contains(data, []byte{0x1b, 0x45}) {
		f = append(f, "bold")
	}
	if bytes.Contains(data, []byte{0x1d, 0x56}) {
		f = append(f, "paper cut")
	}
	if len(f) == 0 {
		return "raw bytes"
	}
	return strings.Join(f, ", ")
}

// textPreview reconstructs the printable text of a job so the receipt is
// legible. It steps over the ESC/POS control sequences the agent emits, so
// their command letters (a, E, V and so on) do not leak into the output.
// Newlines are kept so the printed layout shows.
func textPreview(data []byte) string {
	var b strings.Builder
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == 0x1b: // ESC: @ is 2 bytes, a/E are 3
			if i+1 < len(data) && (data[i+1] == 0x61 || data[i+1] == 0x45) {
				i += 3
			} else {
				i += 2
			}
		case c == 0x1d: // GS: ! is 3 bytes, V (cut) is 4, v (raster) is variable
			i += skipGS(data, i)
		case c == '\n':
			b.WriteByte('\n')
			i++
		case c >= 0x20 && c <= 0x7e:
			b.WriteByte(c)
			i++
		default:
			i++
		}
	}
	out := strings.Trim(b.String(), "\n")
	if out == "" {
		return "(no printable text, image only)"
	}
	return out
}

// skipGS returns how many bytes to advance past a GS sequence starting at i.
func skipGS(data []byte, i int) int {
	if i+1 >= len(data) {
		return 1
	}
	switch data[i+1] {
	case 0x21: // GS ! n  size
		return 3
	case 0x56: // GS V m n  cut
		return 4
	case 0x76: // GS v 0 m xL xH yL yH  raster image, then the bitmap bytes
		if i+8 > len(data) {
			return len(data) - i
		}
		width := int(data[i+4]) + int(data[i+5])<<8  // bytes per row
		height := int(data[i+6]) + int(data[i+7])<<8 // rows
		return 8 + width*height
	default:
		return 2
	}
}

// isTimeout reports whether err is the read deadline firing, which is an
// expected end of job rather than a failure worth logging loudly.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
