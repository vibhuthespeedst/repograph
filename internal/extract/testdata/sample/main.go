package main

import (
	"net/http"

	"example.com/sample/api"
	"example.com/sample/store"
)

func main() {
	_ = http.ListenAndServe(":8080", api.New(&store.DB{}))
}
