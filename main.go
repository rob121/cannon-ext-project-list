package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rob121/cannon/extension"
	"gorm.io/gorm"
)

//go:embed help/*.md
var helpFiles embed.FS

//go:embed all:templates
var templateFiles embed.FS

const (
	extensionName        = "cannon-ext-project-list"
	extensionTitle       = "Project List"
	extensionDescription = "Publishes open camp projects and records when someone adopts one."
	extensionVersion     = "0.1.0"
	updateURLBase        = "https://github.com/rob121/cannon-ext-project-list/releases/download"
	defaultAdminMenu     = "Projects"
	pageSize             = 20
	adoptEvent           = "onProjectListAdopt"

	statusOpen       = "open"
	statusPending    = "pending"
	statusInProgress = "in_progress"
	statusCompleted  = "completed"
	pageSection      = "page"

	defaultPageTitle = "Open Projects"
	defaultPageIntro = "Choose a project to adopt. Adopting it sends your contact details to camp staff so they can coordinate with you."
)

type projectApp struct {
	info      extension.Info
	templates *extension.Templates

	dbMu sync.Mutex
	db   *gorm.DB

	migrateMu sync.Mutex
	migrated  bool
}

type project struct {
	ID         uint      `gorm:"primaryKey;column:project_id"`
	Name       string    `gorm:"column:name;size:200;not null"`
	Skills     string    `gorm:"column:skills;type:text;not null"`
	Note       string    `gorm:"column:note;type:text;not null"`
	CompleteBy string    `gorm:"column:complete_by;size:10"`
	Active     bool      `gorm:"column:active;not null;index"`
	Status     string    `gorm:"column:status;size:32;not null;index"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

type adoption struct {
	ID           uint      `gorm:"primaryKey;column:adoption_id"`
	ProjectID    uint      `gorm:"column:project_id;not null;index"`
	Name         string    `gorm:"column:name;size:200;not null"`
	Email        string    `gorm:"column:email;size:320;not null"`
	Phone        string    `gorm:"column:phone;size:64;not null"`
	Note         string    `gorm:"column:note;type:text"`
	ProposedDate string    `gorm:"column:proposed_date;size:10"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func main() {
	if err := newServer().Run(); err != nil {
		log.Fatal(err)
	}
}

func newServer() *extension.Server {
	info := extension.Info{
		Name:          extensionName,
		Title:         extensionTitle,
		Description:   extensionDescription,
		Version:       extensionVersion,
		UpdateURLBase: updateURLBase,
		AdminMenuName: defaultAdminMenu,
	}
	app := &projectApp{
		info: info,
		templates: extension.NewTemplates(templateFiles, "templates").WithFuncs(template.FuncMap{
			"add": func(a, b int) int { return a + b },
			"sub": func(a, b int) int { return a - b },
		}),
	}
	s := extension.New(info)
	s.EmbedHelp(helpFiles, "help")
	s.EmbedTemplates(templateFiles, "templates")
	s.OnInstall(app.install)
	s.RegisterPage(extension.PageDefinition{
		ID:    "project-list",
		Title: "Project List",
	}, app.page)
	s.HandleData("project/adopt", app.adopt)
	s.HandleAdmin("/admin/", app.admin)
	s.RegisterNotificationEvent(extension.NotificationEventDefinition{
		Event:       adoptEvent,
		Label:       "Project adopted",
		Group:       "project-list",
		GroupLabel:  "Project List",
		Description: "Fired when someone adopts an open project.",
		Placeholders: []extension.NotificationPlaceholder{
			{Key: "project_name", Label: "Project Name"},
			{Key: "skills", Label: "Resources Needed"},
			{Key: "project_note", Label: "Description"},
			{Key: "complete_by", Label: "Complete By"},
			{Key: "name", Label: "Adopter Name"},
			{Key: "email", Label: "Adopter Email"},
			{Key: "phone", Label: "Adopter Phone"},
			{Key: "note", Label: "Adopter Note"},
			{Key: "proposed_date", Label: "Proposed Date"},
			{Key: "admin_url", Label: "Admin URL", Description: "Link to the project"},
			{Key: "site_id", Label: "Site ID"},
		},
	})
	return s
}

func (app *projectApp) install(req extension.WireRequest) error {
	_, err := app.siteDB()
	return err
}

func (app *projectApp) siteDB() (*gorm.DB, error) {
	app.dbMu.Lock()
	if app.db != nil {
		db := app.db
		app.dbMu.Unlock()
		return db, nil
	}
	db, _, err := extension.OpenDB()
	if err != nil {
		app.dbMu.Unlock()
		return nil, err
	}
	app.db = db
	app.dbMu.Unlock()
	if err := app.ensureSchema(db); err != nil {
		return nil, err
	}
	return db, nil
}

func (app *projectApp) ensureSchema(db *gorm.DB) error {
	app.migrateMu.Lock()
	defer app.migrateMu.Unlock()
	if app.migrated {
		return nil
	}
	if err := db.Table(app.projectsTable()).AutoMigrate(&project{}); err != nil {
		return err
	}
	if err := db.Table(app.adoptionsTable()).AutoMigrate(&adoption{}); err != nil {
		return err
	}
	if err := extension.MigrateConfigurationStore(db, app.settingsTable()); err != nil {
		return err
	}
	if err := app.ensureDefaultPage(db); err != nil {
		return err
	}
	app.migrated = true
	return nil
}

func (app *projectApp) page(item string, req extension.WireRequest) extension.WireResponse {
	db, err := app.siteDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	u, err := parseWireURL(req.URL)
	if err != nil {
		return extension.Error(http.StatusBadRequest, err.Error())
	}
	projects, err := app.visibleProjects(db)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	base := pagePath(u)
	views := make([]projectView, 0, len(projects))
	for _, row := range projects {
		views = append(views, projectView{
			project:         row,
			CompleteByLabel: formatCompleteBy(row.CompleteBy),
			AdoptURL:        base + "?adopt=" + strconv.FormatUint(uint64(row.ID), 10),
		})
	}
	title, intro, err := app.loadPageCopy()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	data := map[string]any{
		"Title":     title,
		"Intro":     intro,
		"Projects":  views,
		"Sent":      u.Query().Get("sent") == "1",
		"PageItem":  item,
		"ListURL":   base,
		"CSRFField": csrfField(req),
		"ActionURL": extension.PublicDataURL(extensionName, req.SiteID, "project/adopt"),
		"ReturnURL": base,
	}
	if id, ok := parseID(u.Query().Get("adopt")); ok {
		selected, found := findProject(views, id)
		if !found {
			data["AdoptMissing"] = true
		} else {
			data["Adopt"] = selected
		}
	}
	return app.render("project/list.html", data)
}

func (app *projectApp) visibleProjects(db *gorm.DB) ([]project, error) {
	var rows []project
	err := db.Table(app.projectsTable()).
		Where("active = ? AND status = ?", true, statusOpen).
		Order("CASE WHEN complete_by = '' THEN 1 ELSE 0 END, complete_by, name").
		Find(&rows).Error
	return rows, err
}

func (app *projectApp) adopt(req extension.WireRequest) extension.WireResponse {
	if !strings.EqualFold(req.Method, http.MethodPost) {
		return extension.Error(http.StatusMethodNotAllowed, "POST required")
	}
	values, err := url.ParseQuery(req.Body)
	if err != nil {
		return extension.Error(http.StatusBadRequest, "invalid form submission")
	}
	id, ok := parseID(values.Get("project_id"))
	if !ok {
		return extension.Error(http.StatusBadRequest, "Choose a project to adopt.")
	}
	proposedDate, err := parseProposedDate(values.Get("proposed_date"))
	if err != nil {
		return extension.Error(http.StatusBadRequest, err.Error())
	}
	person := adoption{
		ProjectID:    id,
		Name:         strings.TrimSpace(values.Get("name")),
		Email:        strings.TrimSpace(values.Get("email")),
		Phone:        strings.TrimSpace(values.Get("phone")),
		Note:         strings.TrimSpace(values.Get("note")),
		ProposedDate: proposedDate,
	}
	switch {
	case person.Name == "":
		return extension.Error(http.StatusBadRequest, "Name is required.")
	case person.Email == "":
		return extension.Error(http.StatusBadRequest, "Email is required.")
	case person.Phone == "":
		return extension.Error(http.StatusBadRequest, "Phone is required.")
	}
	if _, err := mail.ParseAddress(person.Email); err != nil {
		return extension.Error(http.StatusBadRequest, "Email must be a valid email address.")
	}

	db, err := app.siteDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	var current project
	err = db.Table(app.projectsTable()).Where("project_id = ?", id).First(&current).Error
	if err == gorm.ErrRecordNotFound {
		return extension.Error(http.StatusNotFound, "Project not found.")
	}
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	if !current.Active || current.Status != statusOpen {
		return extension.Error(http.StatusConflict, "This project is no longer open.")
	}
	if err := db.Table(app.adoptionsTable()).Create(&person).Error; err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}

	redirectURL := strings.TrimSpace(values.Get("return_url"))
	if redirectURL == "" {
		redirectURL = req.URL
	}
	resp := extension.Redirect(http.StatusSeeOther, submitRedirect(redirectURL))
	return extension.WithEmit(resp, adoptEvent, map[string]any{
		"project_name":  current.Name,
		"skills":        current.Skills,
		"project_note":  current.Note,
		"complete_by":   formatCompleteBy(current.CompleteBy),
		"name":          person.Name,
		"email":         person.Email,
		"phone":         person.Phone,
		"note":          person.Note,
		"proposed_date": formatCompleteBy(person.ProposedDate),
		"admin_url":     "/admin/extension-apps/" + extensionName + "/projects/" + strconv.FormatUint(uint64(id), 10),
		"site_id":       req.SiteID,
	})
}

type projectView struct {
	project
	CompleteByLabel string
	AdoptURL        string
	StatusLabel     string
	AdoptionCount   int
}

func findProject(rows []projectView, id uint) (projectView, bool) {
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return projectView{}, false
}

func (app *projectApp) render(name string, data map[string]any) extension.WireResponse {
	body, err := app.templates.Execute(name, data)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	return extension.HTML(http.StatusOK, body)
}

func (app *projectApp) settingsTable() string {
	return extension.TableName(app.info.TablePrefix(), "settings")
}

func (app *projectApp) ensureDefaultPage(db *gorm.DB) error {
	store := extension.DBConfigurationStore(db, app.settingsTable())
	data, err := store.Load(pageSection)
	if err != nil {
		return err
	}
	if _, ok := data["title"]; ok {
		return nil
	}
	return store.Save(pageSection, map[string]any{
		"title":        defaultPageTitle,
		"introduction": defaultPageIntro,
	})
}

func (app *projectApp) loadPageCopy() (string, string, error) {
	db, err := app.siteDB()
	if err != nil {
		return "", "", err
	}
	data, err := extension.DBConfigurationStore(db, app.settingsTable()).Load(pageSection)
	if err != nil {
		return "", "", err
	}
	title, _ := data["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		title = defaultPageTitle
	}
	intro := defaultPageIntro
	if raw, ok := data["introduction"]; ok {
		intro, _ = raw.(string)
		intro = strings.TrimSpace(intro)
	}
	return title, intro, nil
}

func (app *projectApp) projectsTable() string {
	return extension.TableName(app.info.TablePrefix(), "projects")
}

func (app *projectApp) adoptionsTable() string {
	return extension.TableName(app.info.TablePrefix(), "adoptions")
}

func statusLabel(status string) string {
	switch status {
	case statusOpen:
		return "Open"
	case statusPending:
		return "Pending"
	case statusInProgress:
		return "In progress"
	case statusCompleted:
		return "Completed"
	default:
		return status
	}
}

func validStatus(status string) bool {
	switch status {
	case statusOpen, statusPending, statusInProgress, statusCompleted:
		return true
	default:
		return false
	}
}

func formatCompleteBy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return raw
	}
	return parsed.Format("January 2, 2006")
}

func (a adoption) ProposedDateLabel() string {
	return formatCompleteBy(a.ProposedDate)
}

func (a adoption) SubmittedLabel() string {
	if a.CreatedAt.IsZero() {
		return ""
	}
	return a.CreatedAt.UTC().Format("January 2, 2006")
}

func parseProposedDate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", errString("Proposed Date must be a valid date.")
	}
	return raw, nil
}

func parseCompleteBy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", raw); err != nil {
		return "", errString("Complete By must be a valid date.")
	}
	return raw, nil
}

type stringError string

func (e stringError) Error() string { return string(e) }

func errString(message string) error { return stringError(message) }

func csrfField(req extension.WireRequest) template.HTML {
	return template.HTML(extension.CSRFHiddenField(req))
}

func submitRedirect(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || strings.TrimSpace(u.Path) == "" {
		return "/?sent=1"
	}
	q := u.Query()
	q.Set("sent", "1")
	q.Del("adopt")
	u.RawQuery = q.Encode()
	return u.String()
}

func pagePath(u *url.URL) string {
	if u == nil || strings.TrimSpace(u.Path) == "" {
		return "/"
	}
	return u.Path
}

func parsePage(u *url.URL) int {
	page, err := strconv.Atoi(u.Query().Get("page"))
	if err != nil || page < 1 {
		return 1
	}
	return page
}

func parseWireURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return url.Parse("/admin/extension-apps/" + url.PathEscape(extensionName))
	}
	return url.Parse(raw)
}

func adminRoute(path string) (string, []string) {
	if u, err := url.Parse(path); err == nil && u.Path != "" {
		path = u.Path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "admin" && parts[i+1] == "extension-apps" {
			base := "/" + strings.Join(parts[:i+3], "/")
			return base, parts[i+3:]
		}
	}
	return "", nil
}

func parseID(value string) (uint, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint(n), true
}

func likePattern(query string) string {
	query = strings.TrimSpace(query)
	query = strings.NewReplacer("%", "", "_", "").Replace(query)
	return "%" + query + "%"
}
