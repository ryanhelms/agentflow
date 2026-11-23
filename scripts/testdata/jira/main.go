// A read-only Jira issue for the Claude plugin end-to-end test.
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
)

func main() {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rest/api/3/issue/PLAT-123", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"key":"PLAT-123","fields":{"summary":"Fixture GPU quota task","status":{"name":"In Progress"},"issuetype":{"name":"Task"}}}`)
	})
	fmt.Println("http://" + listener.Addr().String())
	log.Fatal(http.Serve(listener, mux))
}
