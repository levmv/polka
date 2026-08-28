package web

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/delivery"
	"github.com/levmv/polka/internal/format"
)

// renderPage executes an embedded HTML template, logging a render failure. By
// the time execution can fail the status and headers are already committed, so
// logging is all that is left — but a silent 200 with a truncated body is worse
// than a log line.
func renderPage(w http.ResponseWriter, tmpl *template.Template, name string, data any) {
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render template %s: %v", name, err)
	}
}

var (
	appTmpl    = pageTemplate("templates/app.html")
	readerTmpl = pageTemplate("templates/reader.html")
	loginTmpl  = pageTemplate("templates/login.html")
	setupTmpl  = pageTemplate("templates/setup.html")
)

func pageTemplate(page string) *template.Template {
	return template.Must(template.ParseFS(templatesFS, "templates/layout.html", "templates/pwa_head.html", page))
}

type layoutPageData struct {
	BookUploadAccept string
}

func newLayoutPageData() layoutPageData {
	return layoutPageData{BookUploadAccept: format.BookUploadAccept()}
}

type appPageData struct {
	layoutPageData
	BootstrapJSON template.JS
}

type appBootstrapDTO struct {
	Me       *MeDTO          `json:"me,omitzero"`
	Settings UserSettingsDTO `json:"settings"`
	// SendEnabled travels with the page so the first render can omit Send without
	// another request.
	SendEnabled bool `json:"send_enabled"`
}

func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	data, err := s.appPageData(r)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", appContentSecurityPolicy)
	renderPage(w, appTmpl, "app.html", data)
}

// The application renders sanitized book-description HTML, so keep script
// execution independently constrained if that output boundary ever regresses.
// Inline styles remain necessary for editor controls and progress indicators.
// Metadata cover previews load directly from the two built-in providers; Open
// Library may redirect its images to the Internet Archive hosts also allowed by
// validateRemoteCoverRedirectURL.
const appContentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https://covers.openlibrary.org " +
	"https://books.google.com https://books.googleusercontent.com " +
	"https://archive.org https://*.archive.org; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'"

func (s *Server) appPageData(r *http.Request) (appPageData, error) {
	userID := UserID(r.Context())
	u, err := s.db.GetUserByID(userID)
	if err != nil {
		return appPageData{}, err
	}
	settings, err := s.db.GetUserSettings(userID)
	if err != nil {
		return appPageData{}, err
	}

	sendEnabled, err := delivery.Enabled(s.db)
	if err != nil {
		return appPageData{}, err
	}

	var me *MeDTO
	if u != nil {
		dto := meDTO(*u)
		me = &dto
	}

	payload, err := json.Marshal(appBootstrapDTO{
		Me:          me,
		Settings:    userSettingsDTO(settings),
		SendEnabled: sendEnabled,
	}, jsontext.EscapeForHTML(true))
	if err != nil {
		return appPageData{}, err
	}
	return appPageData{
		layoutPageData: newLayoutPageData(),
		BootstrapJSON:  template.JS(payload),
	}, nil
}

// setSessionCookie issues the browser session cookie carrying an opaque id.
func setSessionCookie(w http.ResponseWriter, r *http.Request, sid string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sid,
		Path:     "/",
		MaxAge:   int(sessionAbsoluteTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   sessionCookieSecure(r),
	})
}

func sessionCookieSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// No accounts yet: the only meaningful action is creating the first admin.
		if n, err := s.db.CountUsers(); err == nil && n == 0 {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderPage(w, loginTmpl, "login.html", nil)
		return
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		user, err := s.authenticatePassword(r.Context(), r.FormValue("username"), r.FormValue("password"))
		if errors.Is(err, errPasswordAuthBusy) {
			writePasswordAuthBusy(w)
			return
		}
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			serverError(w, err)
			return
		}
		if user == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			renderPage(w, loginTmpl, "login.html", map[string]string{"Error": "Invalid username or password"})
			return
		}

		sid, err := s.sessions.issue(user.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		setSessionCookie(w, r, sid)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// handleLogout revokes the current session and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var revokeErr error
	if c, err := r.Cookie(sessionCookieName); err == nil {
		revokeErr = s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   sessionCookieSecure(r),
	})
	if revokeErr != nil {
		serverError(w, revokeErr)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

// handleSetup creates the first account, always as an admin. It is available
// only while the library has no users; later accounts are managed by an
// authenticated admin or with `polka user`.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.db.CountUsers()
	if err != nil {
		serverError(w, err)
		return
	}
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		renderPage(w, setupTmpl, "setup.html", nil)
		return
	}

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		username := r.FormValue("username")
		password := r.FormValue("password")

		renderErr := func(msg string) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			renderPage(w, setupTmpl, "setup.html", map[string]string{"Error": msg, "Username": username})
		}
		if password != r.FormValue("confirm") {
			renderErr("Passwords do not match")
			return
		}

		user, err := s.db.CreateUser(username, password, db.RoleAdmin)
		if err != nil {
			if errors.Is(err, db.ErrUserExists) {
				renderErr("That username is taken")
				return
			}
			renderErr(err.Error())
			return
		}

		sid, err := s.sessions.issue(user.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		setSessionCookie(w, r, sid)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}
