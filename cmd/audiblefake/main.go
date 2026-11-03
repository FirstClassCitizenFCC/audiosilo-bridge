//go:build audiblefake

// Command audiblefake serves the Audible fake (internal/audible/audiblefake) for
// a live check of the Bridge's Audible flows without an account:
//
//	go run -tags audiblefake ./cmd/audiblefake --listen 127.0.0.1:8099
//	go build -tags embedui,audiblefake -o bin/audiosilo-bridge-fake ./cmd/audiosilo-bridge
//	AUDIOSILO_BRIDGE_AUDIBLE_FAKE_URL=http://127.0.0.1:8099 ./bin/audiosilo-bridge-fake --data ./data
//
// It exists only behind the audiblefake build tag; no release build or image
// carries it.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kodestar/audiosilo-bridge/internal/audible/audiblefake"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address to serve the fake on")
	maxASINs := flag.Int("lastpositions-max", 10, "most ASINs one lastpositions request may carry before a 400")
	flag.Parse()

	fake := audiblefake.New(audiblefake.Options{LastPositionsMax: *maxASINs})
	fmt.Fprintf(os.Stderr, "Audible fake on http://%s (AUDIOSILO_BRIDGE_AUDIBLE_FAKE_URL=http://%s)\n"+
		"controls: GET /_fake/calls, POST /_fake/refuse, GET /_fake/titles,\n"+
		"  POST /_fake/old-profile (every device so far on the older profile: licenses denied as throttled),\n"+
		"  POST /_fake/cut?after=BYTES (the next download drops part way), POST /_fake/expire (download URLs expire),\n"+
		"  POST /_fake/release?asin=ASIN (a preorder comes out today)\n", *listen, *listen)
	srv := &http.Server{Addr: *listen, Handler: fake.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
