// Example: fetch a page with a Chrome TLS fingerprint and print a summary.
//
//	go run ./examples/basic https://tls.peet.ws/api/clean
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/maarkN/cloudscraper-go/pkg/cloudscraper"
)

func main() {
	url := "https://tls.peet.ws/api/clean"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	client, err := cloudscraper.New(
		cloudscraper.WithProfile("chrome"),
		cloudscraper.WithTimeout(20*time.Second),
	)
	if err != nil {
		log.Fatal(err)
	}

	resp, err := client.Get(context.Background(), url)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%s %d — %d bytes from %s\n", resp.Proto, resp.StatusCode, len(resp.Body), resp.URL)
}
