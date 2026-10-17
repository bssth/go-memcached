package memcached

import (
	"bufio"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCache is an in-memory handler. Setting fail makes every call return a server error;
// setting panicOnGet makes Get panic.
type testCache struct {
	mu         sync.Mutex
	items      map[string]*Item
	fail       bool
	panicOnGet bool
}

func newTestCache() *testCache {
	return &testCache{items: map[string]*Item{}}
}

func (c *testCache) Get(key string) MemcachedResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.panicOnGet {
		panic("boom")
	}
	if c.fail {
		return &ServerErrorResponse{"backend down"}
	}
	item, ok := c.items[key]
	if !ok || item.IsExpired() {
		return nil
	}
	return &ItemResponse{Item: item}
}

func (c *testCache) Set(item *Item) MemcachedResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return &ServerErrorResponse{"backend down"}
	}
	c.items[item.Key] = item
	return nil
}

func (c *testCache) Delete(key string) MemcachedResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return &ServerErrorResponse{"backend down"}
	}
	if _, ok := c.items[key]; !ok {
		return notFound{}
	}
	delete(c.items, key)
	return nil
}

// notFound is a non-nil, non-error response: the server replies NOT_FOUND to delete.
type notFound struct{}

func (notFound) WriteResponse(io.Writer) {}

type client struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func startServer(t *testing.T, cache *testCache, maxItemSize int) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	server := NewServer(listener.Addr().String(), cache)
	server.MaxItemSize = maxItemSize
	go func() { _ = server.Serve(listener) }()
	return listener.Addr().String()
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return &client{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *client) send(s string) {
	c.t.Helper()
	if _, err := io.WriteString(c.conn, s); err != nil {
		c.t.Fatal(err)
	}
}

// expect reads one line per wanted string and compares them.
func (c *client) expect(want ...string) {
	c.t.Helper()
	for _, w := range want {
		line, err := c.r.ReadString('\n')
		if err != nil {
			c.t.Fatalf("reading reply (want %q): %v", w, err)
		}
		if got := strings.TrimSuffix(line, "\r\n"); got != w {
			c.t.Fatalf("reply = %q, want %q", got, w)
		}
	}
}

func TestMalformedSetDoesNotCrash(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	for _, line := range []string{
		"set abcdefgh",          // used to panic: index out of range
		"set k 0 0 -5",          // used to panic: makeslice
		"set k 0 0",             // too few fields
		"set k x 0 1",           // bad flags
		"set k 0 y 1",           // bad exptime
		"set k 0 0 z",           // bad length
		"set k 0 0 1 bogus",     // unknown option
		"set k 0 0 99999999999", // length overflows
	} {
		c.send(line + "\r\n")
		c.expect("CLIENT_ERROR bad command line format")
	}

	// The same connection still works.
	c.send("set k 0 0 1\r\nx\r\nget k\r\n")
	c.expect("STORED", "VALUE k 0 1", "x", "END")
}

func TestLongKeyIsRejected(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)
	key := strings.Repeat("k", MaxKeyLength+1)

	c.send("set " + key + " 0 0 1\r\n")
	c.expect("CLIENT_ERROR bad command line format")
	c.send("get " + key + "\r\n")
	c.expect("CLIENT_ERROR bad command line format")
	c.send("delete " + key + "\r\n")
	c.expect("CLIENT_ERROR bad command line format")
}

func TestTooLargeValueIsSkipped(t *testing.T) {
	addr := startServer(t, newTestCache(), 4)
	c := dial(t, addr)

	c.send("set big 0 0 10\r\n0123456789\r\n")
	c.expect("SERVER_ERROR object too large for cache")

	// The data block was skipped, so the connection is still in sync.
	c.send("set small 0 0 4\r\nabcd\r\nget big small\r\n")
	c.expect("STORED", "VALUE small 0 4", "abcd", "END")
}

func TestBadDataChunk(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	c.send("set k 0 0 3\r\nabcde\r\n")
	c.expect("CLIENT_ERROR bad data chunk")
	c.send("version\r\n")
	c.expect("VERSION " + VERSION)
}

func TestSetGetWithFlags(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	c.send("set k 4294967295 0 5\r\nhello\r\n")
	c.expect("STORED")
	c.send("get k\r\n")
	c.expect("VALUE k 4294967295 5", "hello", "END")
	c.send("gets k\r\n")
	c.expect("VALUE k 4294967295 5", "hello", "END")
}

func TestMultiGet(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	c.send("set a 0 0 1\r\n1\r\nset c 0 0 1\r\n3\r\n")
	c.expect("STORED", "STORED")
	c.send("get a b c\r\n")
	c.expect("VALUE a 0 1", "1", "VALUE c 0 1", "3", "END")
}

func TestNoreply(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	// No replies for noreply commands; the get right after sees the value.
	c.send("set k 0 0 1 noreply\r\nx\r\nget k\r\n")
	c.expect("VALUE k 0 1", "x", "END")
	c.send("delete k noreply\r\nget k\r\n")
	c.expect("END")
}

func TestDelete(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	c.send("set k 0 0 1\r\nx\r\n")
	c.expect("STORED")
	c.send("delete k\r\n")
	c.expect("DELETED")
	c.send("delete k\r\n")
	c.expect("NOT_FOUND")
	c.send("delete k 0\r\n")
	c.expect("NOT_FOUND")
	c.send("delete k 10\r\n")
	c.expect("CLIENT_ERROR bad command line format")
	c.send("delete\r\n")
	c.expect("CLIENT_ERROR bad command line format")
}

func TestServerErrorsFromHandler(t *testing.T) {
	cache := newTestCache()
	cache.fail = true
	addr := startServer(t, cache, 0)
	c := dial(t, addr)

	c.send("get k\r\n")
	c.expect("SERVER_ERROR backend down") // no END after an error
	c.send("set k 0 0 1\r\nx\r\n")
	c.expect("SERVER_ERROR backend down")
	c.send("delete k\r\n")
	c.expect("SERVER_ERROR backend down")
	c.send("version\r\n")
	c.expect("VERSION " + VERSION)
}

func TestNegativeExptimeIsExpired(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	c.send("set k 0 -1 1\r\nx\r\n")
	c.expect("STORED")
	c.send("get k\r\n")
	c.expect("END")
}

func TestPanicClosesOnlyThatConnection(t *testing.T) {
	cache := newTestCache()
	cache.panicOnGet = true
	addr := startServer(t, cache, 0)

	c := dial(t, addr)
	c.send("get k\r\n")
	c.expect("SERVER_ERROR internal error")
	if _, err := c.r.ReadString('\n'); err == nil {
		t.Fatal("connection should be closed after a panic")
	}

	// The server keeps accepting connections.
	other := dial(t, addr)
	other.send("version\r\n")
	other.expect("VERSION " + VERSION)
}

func TestUnknownCommands(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	for _, line := range []string{"bogus", "get", "gat 0 k", "stats extra"} {
		c.send(line + "\r\n")
		c.expect("ERROR")
	}
}

func TestStatsCommand(t *testing.T) {
	addr := startServer(t, newTestCache(), 0)
	c := dial(t, addr)

	c.send("stats\r\n")
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "END\r\n" {
			return
		}
		if !strings.HasPrefix(line, "STAT ") {
			t.Fatalf("unexpected stats line %q", line)
		}
	}
}

func TestServerWithoutStats(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	// Built without NewServer, so Stats is nil. This used to panic.
	server := &Server{Handler: newTestCache()}
	go func() { _ = server.Serve(listener) }()

	c := dial(t, listener.Addr().String())
	c.send("set k 0 0 1\r\nx\r\nget k\r\n")
	c.expect("STORED", "VALUE k 0 1", "x", "END")
}
