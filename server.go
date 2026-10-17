// Package memcached provides an interface for building your
// own memcached ascii protocol servers.
package memcached

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"runtime/debug"
	"strconv"
)

const VERSION = "0.0.0"

const (
	// DefaultMaxItemSize is the largest value accepted by set when Server.MaxItemSize is 0.
	// It matches memcached's default item size limit.
	DefaultMaxItemSize = 1024 * 1024
	// MaxKeyLength is the longest key memcached accepts.
	MaxKeyLength = 250
)

var (
	crlf    = []byte("\r\n")
	noreply = []byte("noreply")

	errBadCommandLine = errors.New("bad command line format")
)

type conn struct {
	server *Server
	conn   net.Conn
	rwc    *bufio.ReadWriter
}

type Server struct {
	Addr    string
	Handler RequestHandler
	Stats   Stats
	// MaxItemSize is the largest value accepted by set, in bytes.
	// Zero means DefaultMaxItemSize.
	MaxItemSize int
}

type StorageCmd struct {
	Key     string
	Flags   int
	Exptime int64
	Length  int
	Noreply bool
}

func (s *Server) newConn(rwc net.Conn) (c *conn, err error) {
	c = new(conn)
	c.server = s
	c.conn = rwc
	c.rwc = bufio.NewReadWriter(bufio.NewReaderSize(rwc, 1048576), bufio.NewWriter(rwc))
	return c, nil
}

func (s *Server) maxItemSize() int {
	if s.MaxItemSize > 0 {
		return s.MaxItemSize
	}
	return DefaultMaxItemSize
}

// count adds n to a counter stat. It does nothing if the stat is missing,
// so a Server created without NewServer still works.
func (s *Server) count(name string, n int) {
	if counter, ok := s.Stats[name].(*CounterStat); ok {
		counter.Increment(n)
	}
}

// ListenAndServe starts listening and accepting requests to this server.
func (s *Server) ListenAndServe() error {
	addr := s.Addr
	if addr == "" {
		addr = ":11211"
	}
	l, e := net.Listen("tcp", addr)
	if e != nil {
		return e
	}
	return s.Serve(l)
}

func (s *Server) Serve(l net.Listener) error {
	defer l.Close()
	for {
		rw, e := l.Accept()
		if e != nil {
			return e
		}
		c, err := s.newConn(rw)
		if err != nil {
			continue
		}
		go c.serve()
	}
}

func (c *conn) serve() {
	defer func() {
		// A panic (from a handler or from this package) closes only this
		// connection instead of crashing the whole process.
		if r := recover(); r != nil {
			log.Printf("memcached: panic serving %v: %v\n%s", c.conn.RemoteAddr(), r, debug.Stack())
			_, _ = c.rwc.WriteString("SERVER_ERROR internal error\r\n")
			c.end()
		}
		c.server.count("curr_connections", -1)
		c.Close()
	}()
	c.server.count("total_connections", 1)
	c.server.count("curr_connections", 1)
	for {
		err := c.handleRequest()
		if err != nil {
			if err == io.EOF {
				return
			}
			_, _ = c.rwc.WriteString(err.Error())
			c.end()
		}
	}
}

func (c *conn) end() {
	_ = c.rwc.Flush()
}

func (c *conn) handleRequest() error {
	line, err := c.ReadLine()
	if err != nil || len(line) == 0 {
		return io.EOF
	}
	fields := bytes.Fields(line)
	if len(fields) == 0 {
		return Error
	}
	args := fields[1:]
	switch string(fields[0]) {
	case "get", "gets":
		return c.handleGet(args)
	case "set":
		return c.handleSet(args)
	case "delete":
		return c.handleDelete(args)
	case "stats":
		if len(args) != 0 {
			return Error
		}
		for key, value := range c.server.Stats {
			_, _ = fmt.Fprintf(c.rwc, StatusStat, key, value)
		}
		_, _ = c.rwc.WriteString(StatusEnd)
		c.end()
	case "version":
		_, _ = fmt.Fprintf(c.rwc, StatusVersion, VERSION)
		c.end()
	case "quit":
		return io.EOF
	default:
		return Error
	}
	return nil
}

// handleGet answers "get <key>*" and "gets <key>*".
// CAS values are not supported, so gets replies like get.
func (c *conn) handleGet(keys [][]byte) error {
	getter, ok := c.server.Handler.(Getter)
	if !ok || len(keys) == 0 {
		return Error
	}
	for _, key := range keys {
		if len(key) > MaxKeyLength {
			c.clientError(errBadCommandLine)
			return nil
		}
	}

	responses := make([]MemcachedResponse, 0, len(keys))
	for _, key := range keys {
		c.server.count("cmd_get", 1)
		response := getter.Get(string(key))
		if isErrorResponse(response) {
			// Reply with the error alone, without values or END.
			response.WriteResponse(c.rwc)
			c.end()
			return nil
		}
		if response == nil {
			c.server.count("get_misses", 1)
			continue
		}
		c.server.count("get_hits", 1)
		responses = append(responses, response)
	}

	for _, response := range responses {
		response.WriteResponse(c.rwc)
	}
	_, _ = c.rwc.WriteString(StatusEnd)
	c.end()
	return nil
}

// handleSet answers "set <key> <flags> <exptime> <bytes> [noreply]" followed by a data block.
func (c *conn) handleSet(args [][]byte) error {
	cmd, err := parseStorageCmd(args)
	if err != nil {
		c.clientError(err)
		return nil
	}

	if cmd.Length > c.server.maxItemSize() {
		// Skip the data block so the connection stays in sync.
		if _, err := io.CopyN(io.Discard, c.rwc, int64(cmd.Length)+2); err != nil {
			return io.EOF
		}
		if !cmd.Noreply {
			_, _ = c.rwc.WriteString("SERVER_ERROR object too large for cache\r\n")
			c.end()
		}
		return nil
	}

	value := make([]byte, cmd.Length+2)
	if _, err := c.Read(value); err != nil {
		return io.EOF
	}
	if !bytes.HasSuffix(value, crlf) {
		response := &ClientErrorResponse{"bad data chunk"}
		response.WriteResponse(c.rwc)
		c.end()
		_, _ = c.ReadLine() // Read out the rest of the line
		return nil
	}

	setter, ok := c.server.Handler.(Setter)
	if !ok {
		return Error
	}

	item := &Item{Key: cmd.Key, Flags: cmd.Flags, Value: value[:cmd.Length:cmd.Length]}
	item.SetExpires(cmd.Exptime)

	c.server.count("cmd_set", 1)
	// Handled synchronously even with noreply, so that a following get
	// on the same connection sees the new value.
	response := setter.Set(item)
	if cmd.Noreply {
		return nil
	}
	if response != nil {
		response.WriteResponse(c.rwc)
	} else {
		_, _ = c.rwc.WriteString(StatusStored)
	}
	c.end()
	return nil
}

// handleDelete answers "delete <key> [0] [noreply]".
// A nil response from the handler means DELETED, an error response is sent as is,
// and any other response means NOT_FOUND.
func (c *conn) handleDelete(args [][]byte) error {
	if len(args) == 0 || len(args) > 3 || len(args[0]) > MaxKeyLength {
		c.clientError(errBadCommandLine)
		return nil
	}
	key := string(args[0])
	rest := args[1:]
	noReply := len(rest) > 0 && bytes.Equal(rest[len(rest)-1], noreply)
	if noReply {
		rest = rest[:len(rest)-1]
	}
	// Old clients may send a hold time; only 0 is meaningful today.
	if len(rest) > 1 || (len(rest) == 1 && string(rest[0]) != "0") {
		c.clientError(errBadCommandLine)
		return nil
	}

	deleter, ok := c.server.Handler.(Deleter)
	if !ok {
		return Error
	}

	response := deleter.Delete(key)
	if noReply {
		return nil
	}
	switch {
	case isErrorResponse(response):
		response.WriteResponse(c.rwc)
	case response != nil:
		_, _ = c.rwc.WriteString(StatusNotFound)
	default:
		_, _ = c.rwc.WriteString(StatusDeleted)
	}
	c.end()
	return nil
}

func (c *conn) clientError(err error) {
	response := &ClientErrorResponse{err.Error()}
	response.WriteResponse(c.rwc)
	c.end()
}

func isErrorResponse(response MemcachedResponse) bool {
	switch response.(type) {
	case *ServerErrorResponse, *ClientErrorResponse:
		return true
	default:
		return false
	}
}

func (c *conn) Close() {
	_ = c.conn.Close()
}

func (c *conn) ReadLine() (line []byte, err error) {
	line, _, err = c.rwc.ReadLine()
	return
}

func (c *conn) Read(p []byte) (n int, err error) {
	return io.ReadFull(c.rwc, p)
}

func ListenAndServe(addr string) error {
	s := &Server{
		Addr: addr,
	}
	return s.ListenAndServe()
}

// parseStorageCmd parses the arguments of a storage command:
// <key> <flags> <exptime> <bytes> [noreply]
func parseStorageCmd(args [][]byte) (*StorageCmd, error) {
	if len(args) != 4 && len(args) != 5 {
		return nil, errBadCommandLine
	}
	if len(args[0]) > MaxKeyLength {
		return nil, errBadCommandLine
	}
	flags, err := strconv.ParseUint(string(args[1]), 10, 32)
	if err != nil {
		return nil, errBadCommandLine
	}
	exptime, err := strconv.ParseInt(string(args[2]), 10, 64)
	if err != nil {
		return nil, errBadCommandLine
	}
	length, err := strconv.ParseInt(string(args[3]), 10, 32)
	if err != nil || length < 0 {
		return nil, errBadCommandLine
	}
	if len(args) == 5 && !bytes.Equal(args[4], noreply) {
		return nil, errBadCommandLine
	}
	return &StorageCmd{
		Key:     string(args[0]),
		Flags:   int(flags),
		Exptime: exptime,
		Length:  int(length),
		Noreply: len(args) == 5,
	}, nil
}

// NewServer initializes a new memcached Server
func NewServer(listen string, handler RequestHandler) *Server {
	return &Server{Addr: listen, Handler: handler, Stats: NewStats()}
}
