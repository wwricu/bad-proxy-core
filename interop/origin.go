package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// origin is the target the tunnel is asked to reach. Both halves are patterned
// so a piece dropped, reordered or truncated shows up as a hash mismatch rather
// than as a shorter file nobody noticed.
func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 300*1024)
		for i := range buf {
			buf[i] = byte('a' + i%26)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(buf)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = fmt.Fprintf(w, "len=%d\n", len(body))
		_, _ = w.Write(body)
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:"+os.Args[1], mux))
}
