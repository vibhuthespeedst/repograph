// Package chi is a stand-in for go-chi/chi so the tests need no network.
package chi

import "net/http"

type Router interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
	Route(pattern string, fn func(r Router))
}

type Mux struct{}

func NewRouter() *Mux { return &Mux{} }

func (m *Mux) Get(pattern string, h http.HandlerFunc)       {}
func (m *Mux) Post(pattern string, h http.HandlerFunc)      {}
func (m *Mux) Route(pattern string, fn func(r Router))      { fn(m) }
func (m *Mux) ServeHTTP(http.ResponseWriter, *http.Request) {}
