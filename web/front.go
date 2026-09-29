package web

import (
	"net/http"

	"journal/store"
)

// FrontEnd renders one user interface. The hub picks it from the viewer's ui.
// Until profiles store a ui column, every viewer uses the single registered front-end.
type FrontEnd interface {
	UI() string
	Login(w http.ResponseWriter, r *http.Request, s *Server)
	Logout(w http.ResponseWriter, r *http.Request, s *Server, u store.User)
	Home(w http.ResponseWriter, r *http.Request, s *Server, u store.User)
	Friends(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User, journalUser string)
	Journal(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User, journalUser string)
	Entry(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User, journalUser string, id int64)
	Update(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User)
	Manage(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User)
	Profile(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User, name string)
	Admin(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User)
	Signup(w http.ResponseWriter, r *http.Request, s *Server)
	Recover(w http.ResponseWriter, r *http.Request, s *Server)
	Settings(w http.ResponseWriter, r *http.Request, s *Server, viewer store.User)
}

// Limiter is the request gate. A nil limiter allows every request.
type Limiter interface {
	Allow(r *http.Request) error
}
