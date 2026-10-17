# Memcached library for Go

## What even is this?
Modeled similarly to the stdlib `net/http` package, `memcached` gives you a simple interface to building your own memcached protocol compatible applications.

## About this Fork
This fork was created due to the original library being outdated, with 
the last committed change dating year 2013. 

The author of this fork has committed some changes to update the library and 
ensure its compatibility with the modern Go versions and standarts. In addition, 
a go.mod file has been added to manage dependencies more effectively.

As of now, the library is functional and works seamlessly with the 
current Go version.

## Install
```
$ go get github.com/bssth/go-memcached
```

## Supported commands

`get` and `gets` with one or more keys (CAS is not supported, so `gets` replies like `get`),
`set` (with `noreply`), `delete` (with `noreply`), `stats`, `version`, `quit`.

Malformed commands get `CLIENT_ERROR`, values larger than `Server.MaxItemSize`
(1 MB by default) get `SERVER_ERROR object too large for cache`.
A panic while handling a command closes only that connection.

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

Return values:

| Handler | `nil` | `*ServerErrorResponse` / `*ClientErrorResponse` | anything else |
| --- | --- | --- | --- |
| `Get` | miss | sent instead of the reply, without `END` | written as the value (e.g. `*ItemResponse`) |
| `Set` | `STORED` | sent as is | sent as is |
| `Delete` | `DELETED` | sent as is | `NOT_FOUND` |

Use `&memcached.ServerErrorResponse{Reason: "..."}` when a backend fails.
A negative or past expiration time gives an item that is already expired (`item.IsExpired()`).

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
 * [https://pkg.go.dev/github.com/bssth/go-memcached](https://pkg.go.dev/github.com/bssth/go-memcached)
