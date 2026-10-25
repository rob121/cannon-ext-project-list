package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rob121/cannon/extension"
)

func TestCapabilities(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Capabilities map[string]string `json:"capabilities"`
		Defaults     struct {
			Admin struct {
				MenuName string `json:"menu_name"`
			} `json:"admin"`
		} `json:"defaults"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	for key, path := range map[string]string{
		"page": "/page", "data": "/data", "admin": "/admin",
		"help": "/help", "templates": "/templates", "notifications": "/notifications",
	} {
		if got.Capabilities[key] != path {
			t.Fatalf("capability %s = %q", key, got.Capabilities[key])
		}
	}
	if got.Defaults.Admin.MenuName != defaultAdminMenu {
		t.Fatalf("menu %q", got.Defaults.Admin.MenuName)
	}
}

func TestMeta(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/meta", nil))
	var got struct {
		Name          string `json:"name"`
		Title         string `json:"title"`
		Version       string `json:"version"`
		UpdateURLBase string `json:"update_url_base"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Name != extensionName || got.Title != extensionTitle || got.Version != extensionVersion || got.UpdateURLBase != updateURLBase {
		t.Fatalf("meta %#v", got)
	}
}

func TestTemplates(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/templates", nil))
	var got extension.TemplateListResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"project/list.html": false, "project/admin_list.html": false, "project/admin_view.html": false, "project/admin_form.html": false, "project/admin_page.html": false,
	}
	for _, tmpl := range got.Templates {
		if _, ok := want[tmpl.Path]; ok {
			want[tmpl.Path] = true
		}
	}
	for path, seen := range want {
		if !seen {
			t.Fatalf("missing %s", path)
		}
	}
}

func TestNotificationEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/notifications", nil))
	var got extension.NotificationEventListResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 1 || got.Events[0].Event != adoptEvent {
		t.Fatalf("events %#v", got.Events)
	}
	keys := map[string]bool{}
	for _, placeholder := range got.Events[0].Placeholders {
		keys[placeholder.Key] = true
	}
	for _, key := range []string{"project_name", "skills", "project_note", "complete_by", "name", "email", "phone", "note", "proposed_date", "admin_url", "site_id"} {
		if !keys[key] {
			t.Fatalf("missing placeholder %s", key)
		}
	}
}

func TestInstallCreatesTables(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()
	resp := postWire(t, server, "/install", extension.WireRequest{Method: http.MethodPost, URL: "/install", SiteID: "example"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("install %d: %s", resp.StatusCode, resp.Body)
	}
	db, _, err := extension.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"project_list_settings", "project_list_projects", "project_list_adoptions"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("missing %s", table)
		}
	}
}

func TestPublicPageShowsOnlyActiveOpenProjects(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()
	createProject(t, server, projectFields("Trail clearing", "2026-11-15", true, statusOpen))
	createProject(t, server, projectFields("Hidden inactive", "", false, statusOpen))
	createProject(t, server, projectFields("Already pending", "", true, statusPending))
	createProject(t, server, projectFields("In progress work", "", true, statusInProgress))
	createProject(t, server, projectFields("Finished work", "", true, statusCompleted))
	createProject(t, server, projectFields("Kitchen help", "", true, statusOpen))

	page := renderPage(t, server, "/projects")
	if !strings.Contains(page.Body, "Trail clearing") || !strings.Contains(page.Body, "Kitchen help") {
		t.Fatalf("open projects missing: %s", page.Body)
	}
	descriptionAt := strings.Index(page.Body, ">Description</h3>")
	resourcesAt := strings.Index(page.Body, ">Resources Needed</h3>")
	if descriptionAt < 0 || resourcesAt < descriptionAt || !strings.Contains(page.Body, "Bring gloves and rakes.") {
		t.Fatalf("description should come before resources: %s", page.Body)
	}
	if !strings.Contains(page.Body, "Open Projects") || !strings.Contains(page.Body, defaultPageIntro) {
		t.Fatalf("default page text missing: %s", page.Body)
	}
	if !strings.Contains(page.Body, "Complete By November 15, 2026") {
		t.Fatalf("date missing: %s", page.Body)
	}
	if strings.Contains(page.Body, "Complete By </p>") || strings.Count(page.Body, "Complete By") != 1 {
		t.Fatalf("blank date should stay hidden: %s", page.Body)
	}
	for _, hidden := range []string{"Hidden inactive", "Already pending", "In progress work", "Finished work"} {
		if strings.Contains(page.Body, hidden) {
			t.Fatalf("%s should stay off the public page", hidden)
		}
	}
	if !strings.Contains(page.Body, "Adopt This Project") || !strings.Contains(page.Body, "?adopt=") {
		t.Fatal("missing adopt link")
	}
}

func TestAdoptKeepsProjectOpenAndRecordsEachPerson(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()
	createProject(t, server, projectFields("Paint the lodge", "2026-12-01", true, statusOpen))
	row := projectByName(t, "Paint the lodge")

	missing := postWire(t, server, "/data/project/adopt", extension.WireRequest{
		Method: http.MethodPost,
		URL:    "/projects",
		Body:   adoptBody(row.ID, map[string]string{"phone": ""}),
		SiteID: "example",
	})
	if missing.StatusCode != http.StatusBadRequest || !strings.Contains(missing.Body, "Phone is required.") {
		t.Fatalf("missing phone %#v", missing)
	}

	invalidDate := postWire(t, server, "/data/project/adopt", extension.WireRequest{
		Method: http.MethodPost,
		URL:    "/projects",
		Body:   adoptBody(row.ID, map[string]string{"proposed_date": "next-saturday"}),
		SiteID: "example",
	})
	if invalidDate.StatusCode != http.StatusBadRequest || !strings.Contains(invalidDate.Body, "Proposed Date must be a valid date.") {
		t.Fatalf("invalid date %#v", invalidDate)
	}

	resp := postWire(t, server, "/data/project/adopt", extension.WireRequest{
		Method: http.MethodPost,
		URL:    "/projects?adopt=" + idString(row.ID),
		Body:   adoptBody(row.ID, map[string]string{"note": "I can come Saturday.", "proposed_date": "2026-12-05"}),
		SiteID: "example",
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adopt %d: %s", resp.StatusCode, resp.Body)
	}
	if headerValue(resp, "Location") != "/projects?sent=1" {
		t.Fatalf("location %q", headerValue(resp, "Location"))
	}
	if len(resp.EmitEvents) != 1 || resp.EmitEvents[0].Event != adoptEvent {
		t.Fatalf("emit %#v", resp.EmitEvents)
	}
	args := resp.EmitEvents[0].Arguments
	if args["project_name"] != "Paint the lodge" || args["skills"] != "Bring gloves and rakes." || args["name"] != "Ada Lovelace" {
		t.Fatalf("emit args %#v", args)
	}
	if args["complete_by"] != "December 1, 2026" || args["note"] != "I can come Saturday." || args["proposed_date"] != "December 5, 2026" || args["site_id"] != "example" {
		t.Fatalf("emit details %#v", args)
	}
	if !strings.Contains(args["admin_url"].(string), "/projects/"+idString(row.ID)) {
		t.Fatalf("admin url %#v", args["admin_url"])
	}

	updated := projectByName(t, "Paint the lodge")
	if updated.Status != statusOpen {
		t.Fatalf("status %s", updated.Status)
	}

	page := renderPage(t, server, "/projects?sent=1")
	if !strings.Contains(page.Body, "Paint the lodge") || !strings.Contains(page.Body, "We will contact you to coordinate this project.") {
		t.Fatalf("public page after adopt: %s", page.Body)
	}

	again := postWire(t, server, "/data/project/adopt", extension.WireRequest{
		Method: http.MethodPost,
		Body:   adoptBody(row.ID, map[string]string{"name": "Grace Hopper", "email": "grace@example.com", "phone": "555-0199", "note": "I can help Sunday."}),
		SiteID: "example",
	})
	if again.StatusCode != http.StatusSeeOther {
		t.Fatalf("second adopt %d: %s", again.StatusCode, again.Body)
	}

	detail := postWire(t, server, "/admin/", adminWire(http.MethodGet, adminURL("/projects/"+idString(row.ID))))
	if strings.Contains(detail.Body, `name="name"`) || !strings.Contains(detail.Body, "Edit Project") {
		t.Fatalf("project page should open in view mode: %s", detail.Body)
	}
	if !strings.Contains(detail.Body, "ada@example.com") || !strings.Contains(detail.Body, "grace@example.com") || !strings.Contains(detail.Body, "December 5, 2026") || !strings.Contains(detail.Body, "I can help Sunday.") {
		t.Fatalf("admin view %s", detail.Body)
	}
	editor := postWire(t, server, "/admin/", adminWire(http.MethodGet, adminURL("/projects/"+idString(row.ID)+"/edit")))
	if !strings.Contains(editor.Body, `name="name"`) || !strings.Contains(editor.Body, "Save Project") || !strings.Contains(editor.Body, "admin-form-card-body") {
		t.Fatalf("edit mode %s", editor.Body)
	}
	list := postWire(t, server, "/admin/", adminWire(http.MethodGet, adminURL("")))
	if !strings.Contains(list.Body, ">2<") {
		t.Fatalf("adoption count %s", list.Body)
	}

	closed := projectFields("Paint the lodge", "2026-12-01", true, statusPending)
	moved := postWire(t, server, "/admin/", adminWireWithBody(http.MethodPost, adminURL("/projects/"+idString(row.ID)), closed.Encode()))
	if moved.StatusCode != http.StatusSeeOther {
		t.Fatalf("close %d: %s", moved.StatusCode, moved.Body)
	}
	blocked := postWire(t, server, "/data/project/adopt", extension.WireRequest{
		Method: http.MethodPost,
		Body:   adoptBody(row.ID, map[string]string{"email": "third@example.com"}),
		SiteID: "example",
	})
	if blocked.StatusCode != http.StatusConflict {
		t.Fatalf("closed adopt %d: %s", blocked.StatusCode, blocked.Body)
	}
}

func TestPageTextIsConfigurable(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()
	saved := postWire(t, server, "/admin/", adminWireWithBody(http.MethodPost, adminURL("/settings"), url.Values{
		"title":        {"Camp Projects"},
		"introduction": {"Pick a project and tell us how to reach you."},
	}.Encode()))
	if saved.StatusCode != http.StatusSeeOther {
		t.Fatalf("save page text %d: %s", saved.StatusCode, saved.Body)
	}
	page := renderPage(t, server, "/projects")
	if !strings.Contains(page.Body, "Camp Projects") || !strings.Contains(page.Body, "Pick a project and tell us how to reach you.") {
		t.Fatalf("custom page text missing: %s", page.Body)
	}
	if strings.Contains(page.Body, defaultPageIntro) {
		t.Fatal("default introduction should be replaced")
	}
	editor := postWire(t, server, "/admin/", adminWire(http.MethodGet, adminURL("/settings")))
	if !strings.Contains(editor.Body, "Page Text") || !strings.Contains(editor.Body, "Camp Projects") {
		t.Fatalf("settings form %s", editor.Body)
	}
}

func TestAdminCanReopenProjectAndSearchAdopter(t *testing.T) {
	setupExtensionDB(t)
	server := newServer().Handler()
	denied := postWire(t, server, "/admin/", extension.WireRequest{Method: http.MethodGet, URL: adminURL(""), SiteID: "example"})
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous %d", denied.StatusCode)
	}

	createProject(t, server, projectFields("Repair dock", "", true, statusOpen))
	row := projectByName(t, "Repair dock")
	postWire(t, server, "/data/project/adopt", extension.WireRequest{
		Method: http.MethodPost,
		Body:   adoptBody(row.ID, map[string]string{"email": "sam@example.com", "name": "Music Sam"}),
		SiteID: "example",
	})

	found := postWire(t, server, "/admin/", adminWire(http.MethodGet, adminURL("?q=sam@example.com")))
	if !strings.Contains(found.Body, "Repair dock") || !strings.Contains(found.Body, "1 project") {
		t.Fatalf("search %s", found.Body)
	}

	fields := projectFields("Repair dock", "", true, statusOpen)
	fields.Set("skills", "Bring a hammer.")
	fields.Set("note", "The dock boards are loose.")
	reopened := postWire(t, server, "/admin/", adminWireWithBody(http.MethodPost, adminURL("/projects/"+idString(row.ID)), fields.Encode()))
	if reopened.StatusCode != http.StatusSeeOther {
		t.Fatalf("reopen %d: %s", reopened.StatusCode, reopened.Body)
	}
	page := renderPage(t, server, "/projects")
	if !strings.Contains(page.Body, "Repair dock") || strings.Contains(page.Body, "Complete By") {
		t.Fatalf("reopened page %s", page.Body)
	}
}

func setupExtensionDB(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sites.json")
	dbPath := filepath.Join(dir, "example.sqlite")
	raw := `{
  "install_enabled": false,
  "data_root": ` + jsonString(dir) + `,
  "sites": [{
    "id": "example",
    "name": "Example",
    "host": "http://localhost:8001",
    "database": {"type": "sqlite", "dsn": ` + jsonString(dbPath) + `},
    "template_dir": ` + jsonString(filepath.Join(dir, "templates")) + `,
    "assets_dir": ` + jsonString(filepath.Join(dir, "assets")) + `,
    "tmp_dir": ` + jsonString(filepath.Join(dir, "tmp")) + `,
    "language_dir": ` + jsonString(filepath.Join(dir, "language")) + `
  }]
}`
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANNON_CONFIG", cfgPath)
	oldArgs := os.Args
	os.Args = []string{"project-list", "--site=example", "--socket=/tmp/project-list.sock"}
	t.Cleanup(func() { os.Args = oldArgs })
}

func jsonString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func postWire(t *testing.T, handler http.Handler, path string, wire extension.WireRequest) extension.WireResponse {
	t.Helper()
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d for %s: %s", rec.Code, path, rec.Body.String())
	}
	var resp extension.WireResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func renderPage(t *testing.T, handler http.Handler, rawURL string) extension.WireResponse {
	t.Helper()
	return postWire(t, handler, "/page/project-list", extension.WireRequest{
		Method: http.MethodPost,
		URL:    rawURL,
		CSRF:   "token",
		SiteID: "example",
	})
}

func createProject(t *testing.T, handler http.Handler, fields url.Values) {
	t.Helper()
	resp := postWire(t, handler, "/admin/", adminWireWithBody(http.MethodPost, adminURL("/projects/new"), fields.Encode()))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create %d: %s", resp.StatusCode, resp.Body)
	}
}

func projectFields(name, completeBy string, active bool, status string) url.Values {
	values := url.Values{}
	values.Set("name", name)
	values.Set("skills", "Bring gloves and rakes.")
	values.Set("note", "Meet at the lodge.")
	values.Set("complete_by", completeBy)
	values.Set("status", status)
	if active {
		values.Set("active", "1")
	}
	return values
}

func projectByName(t *testing.T, name string) project {
	t.Helper()
	db, _, err := extension.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	var row project
	if err := db.Table("project_list_projects").Where("name = ?", name).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func adoptBody(id uint, extra map[string]string) string {
	values := url.Values{}
	values.Set("project_id", idString(id))
	values.Set("name", "Ada Lovelace")
	values.Set("email", "ada@example.com")
	values.Set("phone", "555-0100")
	values.Set("return_url", "/projects")
	for key, value := range extra {
		if value == "" {
			values.Del(key)
			continue
		}
		values.Set(key, value)
	}
	return values.Encode()
}

func adminWire(method, rawURL string) extension.WireRequest {
	return adminWireWithBody(method, rawURL, "")
}

func adminWireWithBody(method, rawURL, body string) extension.WireRequest {
	return extension.WireRequest{
		Method: method,
		URL:    rawURL,
		Body:   body,
		CSRF:   "admin-token",
		SiteID: "example",
		User: map[string]any{
			"authenticated": true,
			"email":         "admin@example.com",
		},
	}
}

func adminURL(suffix string) string {
	return "/admin/extension-apps/" + extensionName + suffix
}

func headerValue(resp extension.WireResponse, key string) string {
	for name, values := range resp.Header {
		if strings.EqualFold(name, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func idString(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
