// Command mockcf runs the Cloudflare mock server for manual and browser tests.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/seanyeh77/cf-portal/testdata/mockcf"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18081", "listen address")
	token := flag.String("token", "test-token", "accepted bearer token")
	flag.Parse()
	log.Printf("mock Cloudflare on http://%s/client/v4 (token %q)", *addr, *token)
	log.Fatal(http.ListenAndServe(*addr, mockcf.New(*token)))
}
