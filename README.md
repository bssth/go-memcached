# Memcached library for Go

## What even is this?
Modeled similarly to the stdlib `net/http` package, `memcached` gives you a simple interface to building your own memcached protocol compatible applications.

## Install
```
$ go get github.com/mattrobenolt/go-memcached
```

## Interfaces
Implement as little or as much as you'd like.
```go
type Getter interface {
	RequestHandler
	Get(string) MemcachedResponse
}

type Setter interface {
	RequestHandler
	Set(*Item) MemcachedResponse
}

type Deleter interface {
	RequestHandler
	Delete(string) MemcachedResponse
}
```

## Example

Try this example out by running it and connecting to it with a memcached client.

```go
var (
	listen  = flag.String("l", "", "Interface to listen on. Default to all addresses.")
	port    = flag.Int("p", 11211, "TCP port number to listen on (default: 11211)")
	threads = flag.Int("t", runtime.NumCPU(), fmt.Sprintf("number of threads to use (default: %d)", runtime.NumCPU()))
)

type Cache map[string]*memcached.Item

func (c Cache) Get(key string) memcached.MemcachedResponse {
	if item, ok := c[key]; ok {
		if item.IsExpired() {
			delete(c, key)
		} else {
			return &memcached.ItemResponse{Item: item}
		}
	}
	return nil
}

func (c Cache) Set(item *memcached.Item) memcached.MemcachedResponse {
	c[item.Key] = item
	return nil
}

func (c Cache) Delete(key string) memcached.MemcachedResponse {
	delete(c, key)
	return nil
}

func main() {
	flag.Parse()
	runtime.GOMAXPROCS(*threads)

	address := fmt.Sprintf("%s:%d", *listen, *port)
	server := memcached.NewServer(address, make(Cache))
	log.Fatal(server.ListenAndServe())
}
```

Example connection using telnet:

```bash
telnet localhost 11211
set Test 0 100 10
get Test
```

## Documentation
 * [http://godoc.org/github.com/mattrobenolt/go-memcached](http://godoc.org/github.com/mattrobenolt/go-memcached)
