package wasihttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	outgoinghandler "github.com/ydnar/wasi-http-go/internal/wasi/http/outgoing-handler"
	"github.com/ydnar/wasi-http-go/internal/wasi/http/types"
	"go.bytecodealliance.org/cm"
)

func init() {
	// Override the default net/http transport and client.
	http.DefaultTransport = &Transport{}
	if http.DefaultClient != nil {
		http.DefaultClient.Transport = &Transport{} // TinyGo has custom implementation of net/http
	}
}

// Transport implements [http.RoundTripper] using [wasi-http] APIs.
//
// [wasi-http]: https://github.com/webassembly/wasi-http
type Transport struct{}

// RoundTrip executes a single HTTP transaction.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only close the body if it's not nil
	if req.Body != nil {
		defer req.Body.Close()
	}

	// TODO: wrap this into a helper func outgoingRequest?
	r := types.NewOutgoingRequest(toFields(req.Header))
	r.SetAuthority(cm.Some(requestAuthority(req))) // TODO: when should this be cm.None?
	r.SetMethod(toMethod(req.Method))
	r.SetPathWithQuery(requestPath(req))
	r.SetScheme(cm.Some(toScheme(req.URL.Scheme))) // TODO: when should this be cm.None?

	body, _, _ := r.Body().Result() // the first call should always return OK

	// TODO: when are [options] used?
	// [options]: https://github.com/WebAssembly/wasi-http/blob/main/wit/handler.wit#L38-L39
	incoming, err, isErr := outgoinghandler.Handle(r, cm.None[types.RequestOptions]()).Result()
	if isErr {
		// outgoing request is invalid or not allowed to be made
		return nil, errors.New(err.String())
	}
	defer incoming.ResourceDrop()

	// Write request body
	w2 := newBodyWriter(body, func() http.Header {
		// TODO: extract request trailers
		return nil
	})

	// Only copy from req.Body if it's not nil
	if req.Body != nil {
		fmt.Println(time.Now().Format(time.RFC3339))
		buf := make([]byte, 4096)
		count := 1
		for {
			count++
			fmt.Println(count, " -> ", time.Now().Format(time.RFC3339))

			n, err := req.Body.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				fmt.Println("getting error here")
				return nil, err
			}

			// _, err = w2.Write(buf[:n])
			_, err = io.Copy(w2, bytes.NewReader(buf[:n]))
			if err != nil {
				fmt.Printf("OR >>> KKKK getting error here %#v\n", err)
				return nil, err
			}
		}

		// if _, err := io.Copy(w, req.Body); err != nil {
		// 	fmt.Println(time.Now().Format(time.RFC3339))
		// 	return nil, fmt.Errorf("wasihttp io copy %s: %v", req.Method, err)
		// }
	}
	err2 := w2.finish()
	if err2 != nil {
		return nil, fmt.Errorf("wasihttp w finish: %v", err2)
	}

	// Wait for response
	poll := incoming.Subscribe()
	if !poll.Ready() {
		poll.Block()
	}
	poll.ResourceDrop()

	future := incoming.Get()
	if future.None() {
		return nil, fmt.Errorf("wasihttp: future response is None after blocking")
	}
	// TODO: figure out a better way to handle option<result<result<incoming-response, error-code>>>
	response, err, isErr := future.Some().OK().Result() // the first call should always return OK
	if isErr {
		// TODO: what do we do with the HTTP proxy error-code?
		return nil, errors.New(err.String())
	}
	// TODO: when should an incoming-response be dropped?
	// defer response.ResourceDrop()

	return incomingResponse(response)
}

func requestAuthority(req *http.Request) string {
	if req.Host == "" {
		return req.URL.Host
	} else {
		return req.Host
	}
}

func requestPath(req *http.Request) cm.Option[string] {
	path := req.URL.RequestURI()
	if path == "" {
		return cm.None[string]()
	}
	return cm.Some(path)
}
