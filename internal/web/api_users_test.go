package web

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestAPIUsersAdminListAndCreate(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	member := mustUser(t, database, "Bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodGet, "/api/users", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var users []UserDTO
	if err := json.UnmarshalRead(w.Body, &users); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	if len(users) != 2 || users[0].Username != "admin" || users[1].Username != "bob" {
		t.Fatalf("users = %+v, want admin and bob", users)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, member.ID, http.MethodGet, "/api/users", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member list status = %d, want %d", w.Code, http.StatusForbidden)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/users", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	create := userCreateRequest{Username: "Carol", Password: "newpw", Role: db.RoleMember}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users", create))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var created UserDTO
	if err := json.UnmarshalRead(w.Body, &created); err != nil {
		t.Fatalf("decode created user: %v", err)
	}
	if created.Username != "carol" || created.Role != db.RoleMember || created.ID == "" {
		t.Fatalf("created = %+v, want carol member", created)
	}
	if u, err := database.Authenticate("carol", "newpw"); err != nil || u == nil {
		t.Fatalf("created user cannot authenticate: user=%+v err=%v", u, err)
	}
	kids, err := database.CreateShelf(admin.ID, db.ShelfShared, "Kids", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create scope shelf: %v", err)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users", userCreateRequest{
		Username:      "Dave",
		Password:      "pw",
		Role:          db.RoleReader,
		ContentScope:  db.ContentScopeShelves,
		ScopeShelfIDs: []string{kids.ID},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("scoped create status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	if err := json.UnmarshalRead(w.Body, &created); err != nil {
		t.Fatalf("decode scoped user: %v", err)
	}
	if created.ContentScope != db.ContentScopeShelves || len(created.ScopeShelfIDs) != 1 || created.ScopeShelfIDs[0] != kids.ID {
		t.Fatalf("scoped user = %+v, want only shelf %q", created, kids.ID)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users", userCreateRequest{
		Username: "CAROL",
		Password: "other",
		Role:     db.RoleMember,
	}))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want %d", w.Code, http.StatusConflict)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users", userCreateRequest{
		Username:      "Eve",
		Password:      "pw",
		Role:          db.RoleReader,
		ContentScope:  db.ContentScopeShelves,
		ScopeShelfIDs: []string{"missing"},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope create status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if u, err := database.GetUserByUsername("eve"); err != nil || u != nil {
		t.Fatalf("invalid scope left created user: user=%+v err=%v", u, err)
	}

	unread, err := database.CreateShelf(admin.ID, db.ShelfShared, "Unread", db.ShelfQuery, "status:unread")
	if err != nil {
		t.Fatalf("create status shelf: %v", err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users", userCreateRequest{
		Username:      "Frank",
		Password:      "pw",
		Role:          db.RoleReader,
		ContentScope:  db.ContentScopeShelves,
		ScopeShelfIDs: []string{unread.ID},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status scope create status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	wantReason := "A smart shelf using status: cannot define access because reading status is personal to each account; use a tag-based shelf instead"
	if got := strings.TrimSpace(w.Body.String()); got != wantReason {
		t.Fatalf("status scope error = %q, want %q", got, wantReason)
	}
	if u, err := database.GetUserByUsername("frank"); err != nil || u != nil {
		t.Fatalf("ineligible scope left created user: user=%+v err=%v", u, err)
	}

	if _, err := database.Exec(`
		CREATE TRIGGER fail_broken_user
		BEFORE INSERT ON users WHEN NEW.username = 'broken'
		BEGIN SELECT RAISE(ABORT, 'forced user insert failure'); END
	`); err != nil {
		t.Fatalf("create failing user trigger: %v", err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users", userCreateRequest{
		Username: "Broken",
		Password: "pw",
		Role:     db.RoleMember,
	}))
	if w.Code != http.StatusInternalServerError || strings.TrimSpace(w.Body.String()) != "Internal server error" {
		t.Fatalf("unexpected persistence error = %d %q; want generic 500", w.Code, w.Body.String())
	}
}

func TestAPIUserAccessCanUseAdminPrivateShelf(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	reader := mustUser(t, database, "Reader", db.RoleReader)
	private, err := database.CreateShelf(admin.ID, db.ShelfPersonal, "Kids picks", db.ShelfManual, "")
	if err != nil {
		t.Fatalf("create admin private shelf: %v", err)
	}
	if err := database.AddBookToShelf(private.ID, admin.ID, "w_1"); err != nil {
		t.Fatalf("seed private shelf: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/users/"+reader.ID, userAccessRequest{
		Role:          db.RoleReader,
		ContentScope:  db.ContentScopeShelves,
		ScopeShelfIDs: []string{private.ID},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("scope private shelf status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, reader.ID, http.MethodGet, "/api/books", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("reader books status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var books []BookSummaryDTO
	if err := json.UnmarshalRead(w.Body, &books); err != nil {
		t.Fatalf("decode books: %v", err)
	}
	if len(books) != 1 || books[0].ID != "w_1" {
		t.Fatalf("reader books = %+v, want only w_1", books)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, reader.ID, http.MethodGet, "/api/shelves", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("reader shelves status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var shelves []ShelfDTO
	if err := json.UnmarshalRead(w.Body, &shelves); err != nil {
		t.Fatalf("decode shelves: %v", err)
	}
	if len(shelves) != 0 {
		t.Fatalf("reader shelves = %+v, want hidden admin private scope shelf", shelves)
	}
}

func TestAPIUserPasswordSelfAndAdmin(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	member := mustUser(t, database, "Bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	memberCurrentSession, err := s.sessions.issue(member.ID)
	if err != nil {
		t.Fatalf("issue current member session: %v", err)
	}
	memberOtherSession, err := s.sessions.issue(member.ID)
	if err != nil {
		t.Fatalf("issue other member session: %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequestWithSession(t, memberCurrentSession, http.MethodPost, "/api/users/"+member.ID+"/password", userPasswordRequest{Password: "self-new"}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("self password status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if u, err := database.Authenticate("bob", "self-new"); err != nil || u == nil {
		t.Fatalf("self password was not changed: user=%+v err=%v", u, err)
	}
	assertSessionLive(t, s.sessions, memberCurrentSession, true)
	assertSessionLive(t, s.sessions, memberOtherSession, false)

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, member.ID, http.MethodPost, "/api/users/"+admin.ID+"/password", userPasswordRequest{Password: "stolen"}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member reset admin status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if u, err := database.Authenticate("admin", "pw"); err != nil || u == nil {
		t.Fatalf("admin password unexpectedly changed: user=%+v err=%v", u, err)
	}

	memberResetSession, err := s.sessions.issue(member.ID)
	if err != nil {
		t.Fatalf("issue reset member session: %v", err)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users/"+member.ID+"/password", userPasswordRequest{Password: "admin-reset"}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("admin reset status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if u, err := database.Authenticate("bob", "admin-reset"); err != nil || u == nil {
		t.Fatalf("admin reset password was not applied: user=%+v err=%v", u, err)
	}
	assertSessionLive(t, s.sessions, memberResetSession, false)

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users/"+member.ID+"/password", userPasswordRequest{}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty password status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPost, "/api/users/"+member.ID+"/password", userPasswordRequest{Password: strings.Repeat("x", 73)}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long password status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestAPIUserDeleteGuardsLastAdminAndRevokesSessions(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	admin := mustUser(t, database, "Admin", db.RoleAdmin)
	member := mustUser(t, database, "Bob", db.RoleMember)

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, member.ID, http.MethodDelete, "/api/users/"+admin.ID, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("member delete status = %d, want %d", w.Code, http.StatusForbidden)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodPatch, "/api/users/"+admin.ID, userAccessRequest{Role: db.RoleMember}))
	if w.Code != http.StatusConflict {
		t.Fatalf("last admin demote status = %d, want %d; body: %s", w.Code, http.StatusConflict, w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodDelete, "/api/users/"+admin.ID, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("last admin delete status = %d, want %d; body: %s", w.Code, http.StatusConflict, w.Body.String())
	}

	memberSession, err := s.sessions.issue(member.ID)
	if err != nil {
		t.Fatalf("issue member session: %v", err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodDelete, "/api/users/"+member.ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete member status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
	if u, err := database.GetUserByID(member.ID); err != nil || u != nil {
		t.Fatalf("deleted member still present: user=%+v err=%v", u, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: memberSession})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user's session status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	admin2 := mustUser(t, database, "OtherAdmin", db.RoleAdmin)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, admin.ID, http.MethodDelete, "/api/users/"+admin2.ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete second admin status = %d, want %d; body: %s", w.Code, http.StatusNoContent, w.Body.String())
	}
}

func jsonRequest(t *testing.T, s *Server, userID, method, target string, payload any) *http.Request {
	t.Helper()

	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		sid, err := s.sessions.issue(userID)
		if err != nil {
			t.Fatalf("issue session: %v", err)
		}
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	}
	return req
}

func jsonRequestWithSession(t *testing.T, sid, method, target string, payload any) *http.Request {
	t.Helper()

	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	return req
}
