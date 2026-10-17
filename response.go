package memcached

import (
	"fmt"
	"io"
)

type MemcachedResponse interface {
	WriteResponse(io.Writer)
}

type ItemResponse struct {
	Item *Item
}

func (r *ItemResponse) WriteResponse(writer io.Writer) {
	_, _ = fmt.Fprintf(writer, StatusValue, r.Item.Key, r.Item.Flags, len(r.Item.Value))
	_, _ = writer.Write(r.Item.Value)
	_, _ = writer.Write(crlf)
}

type BulkResponse struct {
	Responses []MemcachedResponse
}

func (r *BulkResponse) WriteResponse(writer io.Writer) {
	for _, response := range r.Responses {
		if response != nil {
			response.WriteResponse(writer)
		}
	}
}

// ServerErrorResponse tells the client that the server could not handle the
// command, for example because a backend is down. Handlers can return it from
// Get, Set and Delete.
type ServerErrorResponse struct {
	Reason string
}

func (r *ServerErrorResponse) WriteResponse(writer io.Writer) {
	_, _ = fmt.Fprintf(writer, StatusServerErrorReason, r.Reason)
}

type ClientErrorResponse struct {
	Reason string
}

func (r *ClientErrorResponse) WriteResponse(writer io.Writer) {
	_, _ = fmt.Fprintf(writer, StatusClientError, r.Reason)
}
