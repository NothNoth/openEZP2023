// Command ezp-serve serves the EZP2023+ web UI on localhost.
// WebUSB only works in a secure context; http://localhost counts as one.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"ezp2023/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8023", "listen address (keep it on localhost for WebUSB)")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	fs := http.FileServerFS(web.Files)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fs.ServeHTTP(w, r)
	})
	fmt.Printf("EZP2023+ UI: http://%s/  (open it in Chrome, Edge or Opera)\n", ln.Addr())
	log.Fatal(http.Serve(ln, mux))
}
