/*******************************************************************************
 *     ___                  _   ____  ____
 *    / _ \ _   _  ___  ___| |_|  _ \| __ )
 *   | | | | | | |/ _ \/ __| __| | | |  _ \
 *   | |_| | |_| |  __/\__ \ |_| |_| | |_) |
 *    \__\_\\__,_|\___||___/\__|____/|____/
 *
 *  Copyright (c) 2014-2019 Appsicle
 *  Copyright (c) 2019-2022 QuestDB
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *  http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 *
 ******************************************************************************/

package questdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// NewLineSender creates new InfluxDB Line Protocol (ILP) sender. Each
// sender corresponds to a single TCP connection. Sender should
// not be called concurrently by multiple goroutines.
func NewLineSender(ctx context.Context, opts ...LineSenderOption) (*LineSender, error) {
	var d net.Dialer
	s := &LineSender{
		address: "127.0.0.1:9009",
		bufCap:  32 * 1024,
	}
	for _, opt := range opts {
		opt(s)
	}
	conn, err := d.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %v", err)
	}
	s.conn = conn
	s.buf = bytes.NewBuffer(make([]byte, 0, s.bufCap))
	return s, nil
}

// LineSender allows you to insert rows into QuestDB by sending ILP
// messages.
type LineSender struct {
	address    string
	bufCap     int
	conn       net.Conn
	buf        *bytes.Buffer
	lastMsgPos int
	lastErr    error
	hasTable   bool
	hasFields  bool
}

// LineSenderOption defines line sender option.
type LineSenderOption func(*LineSender)

// WithAddress sets address to connect to. Should be in the
// "host:port" format. Defaults to "127.0.0.1:9009".
func WithAddress(address string) LineSenderOption {
	return func(s *LineSender) {
		s.address = address
	}
}

// WithBufferCapacity sets desired buffer capacity in bytes to
// be used when sending ILP messages. This is a soft limit, i.e.
// the underlying buffer may grow larger than the provided value,
// but will shrink once Flush is called.
func WithBufferCapacity(capacity int) LineSenderOption {
	return func(s *LineSender) {
		if capacity > 0 {
			s.bufCap = capacity
		}
	}
}

// Close closes the underlying TCP connection. Does not flush
// in-flight messages, so make sure to call Flush first.
func (s *LineSender) Close() error {
	return s.conn.Close()
}

// Table sets the table name (metric) for a new ILP message. Should be
// called before any Symbol or Field method.
func (s *LineSender) Table(name string) *LineSender {
	if s.lastErr != nil {
		return s
	}
	// TODO validate table name:
	// Table name and columns name must not contain any of the forbidden characters: ., ?,,,:,\,/,\0,),(,+,*,~,% and -.
	if s.hasTable {
		s.lastErr = errors.New("table name already provided")
		return s
	}
	s.lastErr = s.writeStrName(name)
	if s.lastErr != nil {
		return s
	}
	s.hasTable = true
	return s
}

// Symbol adds a symbol column (tag) value to the ILP message. Should be
// called before any Field method.
func (s *LineSender) Symbol(name, val string) *LineSender {
	if s.lastErr != nil {
		return s
	}
	// TODO validate column name:
	// Table name and columns name must not contain any of the forbidden characters: ., ?,,,:,\,/,\0,),(,+,*,~,% and -.
	if !s.hasTable {
		s.lastErr = errors.New("table name was not provided")
		return s
	}
	if s.hasFields {
		s.lastErr = errors.New("symbols have to be written before any other column")
		return s
	}
	s.buf.WriteByte(',')
	s.lastErr = s.writeStrName(name)
	if s.lastErr != nil {
		return s
	}
	s.buf.WriteByte('=')
	s.lastErr = s.writeStrValue(val, false)
	if s.lastErr != nil {
		return s
	}
	return s
}

// IntColumn adds a long column (integer field) value to
// the ILP message.
func (s *LineSender) IntColumn(name string, val int64) *LineSender {
	if !s.prepareForField(name) {
		return s
	}
	// TODO validate NaN and infinity values
	s.lastErr = s.writeStrName(name)
	if s.lastErr != nil {
		return s
	}
	s.buf.WriteByte('=')
	// TODO implement proper serialization for numbers
	s.buf.WriteString(fmt.Sprintf("%d", val))
	s.buf.WriteByte('i')
	s.hasFields = true
	return s
}

// FloatColumn adds a double column (float field) value to
// the ILP message.
func (s *LineSender) FloatColumn(name string, val float64) *LineSender {
	if !s.prepareForField(name) {
		return s
	}
	// TODO validate NaN and infinity values
	s.lastErr = s.writeStrName(name)
	if s.lastErr != nil {
		return s
	}
	s.buf.WriteByte('=')
	// TODO implement proper serialization for numbers
	s.buf.WriteString(fmt.Sprintf("%f", val))
	s.hasFields = true
	return s
}

// StringColumn adds a string column (field) value to the ILP message.
func (s *LineSender) StringColumn(name, val string) *LineSender {
	if !s.prepareForField(name) {
		return s
	}
	s.lastErr = s.writeStrName(name)
	if s.lastErr != nil {
		return s
	}
	s.buf.WriteByte('=')
	s.buf.WriteByte('"')
	s.lastErr = s.writeStrValue(val, true)
	if s.lastErr != nil {
		return s
	}
	s.buf.WriteByte('"')
	s.hasFields = true
	return s
}

// BoolColumn adds a boolean column (field) value to the ILP message.
func (s *LineSender) BoolColumn(name string, val bool) *LineSender {
	if !s.prepareForField(name) {
		return s
	}
	s.lastErr = s.writeStrName(name)
	if s.lastErr != nil {
		return s
	}
	s.buf.WriteByte('=')
	if val {
		s.buf.WriteByte('t')
	} else {
		s.buf.WriteByte('f')
	}
	s.hasFields = true
	return s
}

// TODO introduce ErrInvalidMsg

func (s *LineSender) writeStrName(str string) error {
	// Since we're interested in ASCII chars, it's fine to iterate
	// through bytes instead of runes.
	for i := 0; i < len(str); i++ {
		b := str[i]
		switch b {
		case ' ':
			s.buf.WriteByte('\\')
		case '=':
			s.buf.WriteByte('\\')
		case '"':
			s.buf.WriteByte('\\')
		case '\n':
			return fmt.Errorf("new line chars are not allowed in string values: %s", str)
		case '\r':
			return fmt.Errorf("carriage return chars are not allowed in string values: %s", str)
		default:
			if illegalNameChar(b) {
				return fmt.Errorf("table or column name contains one of illegal chars: '.', '?', ',', ':', '\\', '/', '\\0', ')', '(', '+', '*', '~', '%%', '-': %s", str)
			}
		}
		s.buf.WriteByte(b)
	}
	return nil
}

func illegalNameChar(ch byte) bool {
	switch ch {
	case '.':
		return true
	case '?':
		return true
	case ',':
		return true
	case ':':
		return true
	case '\\':
		return true
	case '/':
		return true
	case ')':
		return true
	case '(':
		return true
	case '+':
		return true
	case '*':
		return true
	case '~':
		return true
	case '%':
		return true
	case '-':
		return true
	case '\x00':
		return true
	}
	return false
}

func (s *LineSender) writeStrValue(str string, quoted bool) error {
	// Since we're interested in ASCII chars, it's fine to iterate
	// through bytes instead of runes.
	for i := 0; i < len(str); i++ {
		b := str[i]
		switch b {
		case ' ':
			if !quoted {
				s.buf.WriteByte('\\')
			}
		case ',':
			if !quoted {
				s.buf.WriteByte('\\')
			}
		case '=':
			if !quoted {
				s.buf.WriteByte('\\')
			}
		case '"':
			s.buf.WriteByte('\\')
		case '\\':
			s.buf.WriteByte('\\')
		case '\n':
			return fmt.Errorf("new line chars are not allowed in string values: %s", str)
		case '\r':
			return fmt.Errorf("carriage return chars are not allowed in string values: %s", str)
		}
		s.buf.WriteByte(b)
	}
	return nil
}

func (s *LineSender) prepareForField(name string) bool {
	if s.lastErr != nil {
		return false
	}
	if !s.hasTable {
		s.lastErr = errors.New("table name was not provided")
		return false
	}
	if !s.hasFields {
		s.buf.WriteByte(' ')
	} else {
		s.buf.WriteByte(',')
	}
	return true
}

// AtNow omits the timestamp and finalizes the ILP message.
// The server will insert each message using the system clock
// as the row timestamp.
//
// If the underlying buffer reaches configured capacity, this
// method also sends the accumulated messages.
func (s *LineSender) AtNow(ctx context.Context) error {
	return s.At(ctx, -1)
}

// At sets the timestamp in Epoch nanoseconds and finalizes
// the ILP message. A negative ts value gets ignored making
// this call behave in the same way as AtNow.
//
// If the underlying buffer reaches configured capacity, this
// method also sends the accumulated messages.
func (s *LineSender) At(ctx context.Context, ts int64) error {
	err := s.lastErr
	s.lastErr = nil
	if err != nil {
		// Discard the partially written message.
		s.buf.Truncate(s.lastMsgPos)
		return err
	}
	if !s.hasTable {
		return errors.New("table name was not provided")
	}

	if ts > -1 {
		s.buf.WriteByte(' ')
		// TODO implement proper serialization for numbers
		s.buf.WriteString(fmt.Sprintf("%d", ts))
	}
	s.buf.WriteByte('\n')

	s.lastMsgPos = s.buf.Len()
	s.hasTable = false
	s.hasFields = false

	if s.buf.Len() > s.bufCap {
		return s.Flush(ctx)
	}
	return nil
}

// Flush flushes the accumulated messages to the underlying TCP
// connection. Should be called periodically to make sure that
// all messages are sent to the server.
//
// For optimal performance, this method should not be called after
// each ILP message. Instead, the messages should be written in
// batches followed by a Flush call.
func (s *LineSender) Flush(ctx context.Context) error {
	err := s.lastErr
	s.lastErr = nil
	if err != nil {
		return err
	}

	if err = ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		s.conn.SetWriteDeadline(deadline)
	} else {
		s.conn.SetWriteDeadline(time.Time{})
	}

	n, err := s.buf.WriteTo(s.conn)
	if err != nil {
		s.lastMsgPos -= int(n)
		return err
	}

	if s.buf.Cap() > s.bufCap {
		// Shrink the buffer back to desired capacity.
		s.buf = bytes.NewBuffer(make([]byte, 0, s.bufCap))
	}
	s.lastMsgPos = 0

	return nil
}

// Messages returns a copy of accumulated ILP messages that are not
// flushed to the TCP connection yet. Useful for debugging purposes.
func (s *LineSender) Messages() string {
	return s.buf.String()
}
