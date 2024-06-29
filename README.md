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
	Get(string) (*Item, error)
}

type Setter interface {
	RequestHandler
	Set(*Item) error
}

type Deleter interface {
	RequestHandler
	Delete(string) error
}
```

## Hello World
```go
package main

import (
	memcached "github.com/mattrobenolt/go-memcached"
)

type Cache struct {}

func (c *Cache) Get(key string) (item *memcached.Item, err error) {
	if key == "hello" {
		item = &memcached.Item{
			Key: key,
			Value: []byte("world"),
		}
		return item, nil
	}
	return nil, memcached.NotFound
}

func main() {
	server := memcached.NewServer(":11211", &Cache{})
	server.ListenAndServe()
}
```

## Examples

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
