// Package protocol implements git's smart HTTP transport: pkt-line framing,
// reference advertisement, and the upload-pack and receive-pack services.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Pkt-line is git's framing for everything on the wire. Each packet is a
// four-character hex length covering the length field itself, then that many
// bytes minus four of payload. Three lengths are reserved as signals rather
// than data.
const (
	// FlushPkt ends a section. The sender has said all it intends to for now.
	FlushPkt = "0000"
	// DelimPkt separates sections within one request in protocol v2.
	DelimPkt = "0001"
	// ResponseEndPkt ends a v2 response.
	ResponseEndPkt = "0002"

	// MaxPayload is the largest payload one packet can carry. The length
	// field is four hex digits, so a packet cannot exceed 65520 bytes, and
	// four of those are the length itself.
	MaxPayload = 65516
)

// Special packet kinds, returned by Reader.Read in place of a payload.
type Kind uint8

const (
	KindData Kind = iota
	KindFlush
	KindDelim
	KindResponseEnd
)

// ErrShortPacket reports a malformed length prefix or a truncated payload.
var ErrShortPacket = errors.New("pktline: malformed packet")

// Reader decodes a pkt-line stream.
type Reader struct {
	r   *bufio.Reader
	buf []byte
}

// NewReader wraps r. The returned Reader buffers, so callers should not also
// read from r directly.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 32*1024)}
}

// Read returns the next packet.
//
// The payload is only valid until the next call: it points into a buffer the
// Reader reuses, which keeps a long negotiation from allocating once per
// line. Callers that keep a payload must copy it.
func (p *Reader) Read() (Kind, []byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(p.r, lenBuf[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return KindData, nil, fmt.Errorf("%w: truncated length prefix", ErrShortPacket)
		}
		return KindData, nil, err
	}

	var n16 [2]byte
	if _, err := hex.Decode(n16[:], lenBuf[:]); err != nil {
		return KindData, nil, fmt.Errorf("%w: length %q is not hex", ErrShortPacket, lenBuf)
	}
	n := int(n16[0])<<8 | int(n16[1])

	switch n {
	case 0:
		return KindFlush, nil, nil
	case 1:
		return KindDelim, nil, nil
	case 2:
		return KindResponseEnd, nil, nil
	case 3:
		return KindData, nil, fmt.Errorf("%w: length 3 is reserved", ErrShortPacket)
	}
	if n < 4 {
		return KindData, nil, fmt.Errorf("%w: length %d is below the 4-byte minimum", ErrShortPacket, n)
	}

	size := n - 4
	if cap(p.buf) < size {
		p.buf = make([]byte, size)
	}
	p.buf = p.buf[:size]
	if _, err := io.ReadFull(p.r, p.buf); err != nil {
		return KindData, nil, fmt.Errorf("%w: payload: %v", ErrShortPacket, err)
	}
	return KindData, p.buf, nil
}

// ReadLine returns the next data packet as a string with any trailing
// newline removed, which is how git terminates most text packets.
//
// It returns ok=false at a flush, so the usual negotiation loop reads until
// the sender flushes without having to match on kinds.
func (p *Reader) ReadLine() (line string, ok bool, err error) {
	kind, payload, err := p.Read()
	if err != nil {
		return "", false, err
	}
	if kind != KindData {
		return "", false, nil
	}
	if len(payload) > 0 && payload[len(payload)-1] == '\n' {
		payload = payload[:len(payload)-1]
	}
	return string(payload), true, nil
}

// Writer encodes a pkt-line stream.
type Writer struct {
	w   io.Writer
	err error
}

// NewWriter wraps w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Err returns the first error the Writer hit.
//
// Write methods are no-ops after a failure, so a long response can be
// composed without checking every call and the error read once at the end.
func (p *Writer) Err() error { return p.err }

// WritePacket frames and writes one payload.
func (p *Writer) WritePacket(payload []byte) error {
	if p.err != nil {
		return p.err
	}
	if len(payload) > MaxPayload {
		p.err = fmt.Errorf("pktline: payload of %d bytes exceeds the %d-byte maximum", len(payload), MaxPayload)
		return p.err
	}
	var hdr [4]byte
	hex.Encode(hdr[:], []byte{byte((len(payload) + 4) >> 8), byte(len(payload) + 4)})
	if _, p.err = p.w.Write(hdr[:]); p.err != nil {
		return p.err
	}
	_, p.err = p.w.Write(payload)
	return p.err
}

// WriteString frames a string payload.
func (p *Writer) WriteString(s string) error { return p.WritePacket([]byte(s)) }

// WriteLine frames a string, appending the newline git expects on text
// packets.
func (p *Writer) WriteLine(s string) error { return p.WriteString(s + "\n") }

// WriteFlush writes the flush packet.
func (p *Writer) WriteFlush() error { return p.writeSpecial(FlushPkt) }

// WriteDelim writes the delimiter packet.
func (p *Writer) WriteDelim() error { return p.writeSpecial(DelimPkt) }

func (p *Writer) writeSpecial(s string) error {
	if p.err != nil {
		return p.err
	}
	_, p.err = io.WriteString(p.w, s)
	return p.err
}

// Sideband channels. A packed response multiplexes three streams over one
// connection so progress and errors can reach the client while pack data is
// still flowing.
const (
	SidebandData     byte = 1 // packfile bytes
	SidebandProgress byte = 2 // human-readable progress, shown by the client
	SidebandError    byte = 3 // a fatal error, shown and then the client aborts
)

// WriteSideband frames a payload on one sideband channel.
func (p *Writer) WriteSideband(channel byte, payload []byte) error {
	if p.err != nil {
		return p.err
	}
	// One byte of the packet goes to the channel number, so a full-size
	// payload has to be split.
	for len(payload) > 0 {
		n := len(payload)
		if n > MaxPayload-1 {
			n = MaxPayload - 1
		}
		framed := make([]byte, n+1)
		framed[0] = channel
		copy(framed[1:], payload[:n])
		if err := p.WritePacket(framed); err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

// SidebandWriter adapts one sideband channel to io.Writer, so a packfile can
// be streamed into it without being buffered whole.
type SidebandWriter struct {
	W       *Writer
	Channel byte
}

func (s SidebandWriter) Write(b []byte) (int, error) {
	if err := s.W.WriteSideband(s.Channel, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// WriteError sends a fatal error on the error channel.
func (p *Writer) WriteError(msg string) error {
	return p.WriteSideband(SidebandError, []byte(msg+"\n"))
}

// Rest returns a reader over everything after the packets already read.
//
// A push sends its reference commands as pkt-lines and then the packfile as
// raw bytes on the same stream. By that point the buffered reader has
// almost certainly pulled part of the packfile in already, so handing back
// the underlying reader alone would lose those bytes. This stitches the
// buffer back in front of it.
func (p *Reader) Rest() io.Reader {
	if n := p.r.Buffered(); n > 0 {
		buffered, _ := p.r.Peek(n)
		head := make([]byte, n)
		copy(head, buffered)
		_, _ = p.r.Discard(n)
		return io.MultiReader(bytes.NewReader(head), p.r)
	}
	return p.r
}
