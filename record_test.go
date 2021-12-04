package fakeconn

import (
	"io"
	"net"
	"net/http"
	"os"
	"sync"
)

func ExampleRecordListen() {
	l, _ := net.Listen("tcp", "127.1.2.3:3000")
	l = RecordListen(l, os.Stdout)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", "Fri, 03 Dec 2021 00:00:00 GMT")
		io.WriteString(w, "Hello, World!")
	})

	var wg sync.WaitGroup
	defer wg.Wait()

	var server http.Server
	defer server.Close()

	wg.Add(1)
	go func() {
		defer wg.Done()
		server.Serve(l)
	}()

	resp, _ := http.Get("http://127.1.2.3:3000/")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Output:
	// <- GET / HTTP/1.1\r\n
	// <- Host: 127.1.2.3:3000\r\n
	// <- User-Agent: Go-http-client/1.1\r\n
	// <- Accept-Encoding: gzip\r\n
	// <- \r\n
	// -> HTTP/1.1 200 OK\r\n
	// -> Date: Fri, 03 Dec 2021 00:00:00 GMT\r\n
	// -> Content-Length: 13\r\n
	// -> Content-Type: text/plain; charset=utf-8\r\n
	// -> \r\n
	// -> Hello, World!
}
