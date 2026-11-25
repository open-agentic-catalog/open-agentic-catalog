// This native package-test fixture can substitute one identifier after a real manifest read.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

func main() {
	switch os.Args[1] {
	case "--help":
		fmt.Println("Fixture CLI")
	case "version":
		fmt.Println(os.Getenv("FIXTURE_CLI_BUILD"))
	case "discovery":
		if len(os.Args) != 5 || os.Args[2] != "manifest" || os.Args[3] != "--base-url" {
			panic("unexpected discovery arguments")
		}
		resp, err := http.Get(os.Args[4] + "/.well-known/ard.json")
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		var manifest struct {
			Entries []map[string]string `json:"entries"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
			panic(err)
		}
		if id := os.Getenv("FIXTURE_DISCOVERY_IDENTIFIER"); id != "" {
			manifest.Entries[0]["identifier"] = id
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
			"data": manifest, "meta": map[string]string{"requestId": resp.Header.Get("X-Request-ID")},
		}); err != nil {
			panic(err)
		}
	default:
		panic("unexpected fixture command: " + os.Args[1])
	}
}
