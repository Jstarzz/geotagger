package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 2 || strings.Contains(os.Args[1], ".") || strings.Contains(os.Args[1], ":") {
		fmt.Fprintln(os.Stderr, "usage: keygen <key-id>  (id may not contain '.' or ':')")
		os.Exit(2)
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		panic(err)
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	digest := sha256.Sum256([]byte(secret))
	id := os.Args[1]
	fmt.Printf("client token: %s.%s\n", id, secret)
	fmt.Printf("API_KEYS entry: %s:%s\n", id, hex.EncodeToString(digest[:]))
}
