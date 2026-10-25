package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rob121/cannon/extension"
	"gorm.io/gorm"
)

func (app *projectApp) admin(req extension.WireRequest) extension.WireResponse {
	if req.User == nil || !extension.UserAuthenticated(req) {
		return extension.Error(http.StatusForbidden, "sign in required")
	}
	base, parts := adminRoute(req.URL)
	if base == "" {
		return extension.Error(http.StatusNotFound, "unknown admin route")
	}
	switch {
	case len(parts) == 0 || (len(parts) == 1 && parts[0] == ""):
		return app.listProjects(req, base)
	case len(parts) == 1 && parts[0] == "projects" && req.Method == http.MethodGet:
		return app.listProjects(req, base)
	case len(parts) == 1 && parts[0] == "settings" && req.Method == http.MethodGet:
		return app.editPageText(req, base)
	case len(parts) == 1 && parts[0] == "settings" && req.Method == http.MethodPost:
		return app.savePageText(req, base)
	case len(parts) == 2 && parts[0] == "projects" && parts[1] == "new" && req.Method == http.MethodGet:
		return app.editProject(req, base, 0)
	case len(parts) == 2 && parts[0] == "projects" && parts[1] == "new" && req.Method == http.MethodPost:
		return app.saveProject(req, base, 0)
	case len(parts) == 2 && parts[0] == "projects" && req.Method == http.MethodGet:
		id, ok := parseID(parts[1])
		if !ok {
			return extension.Error(http.StatusNotFound, "project not found")
		}
		return app.viewProject(req, base, id)
	case len(parts) == 2 && parts[0] == "projects" && req.Method == http.MethodPost:
		id, ok := parseID(parts[1])
		if !ok {
			return extension.Error(http.StatusNotFound, "project not found")
		}
		return app.saveProject(req, base, id)
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "edit" && req.Method == http.MethodGet:
		id, ok := parseID(parts[1])
		if !ok {
			return extension.Error(http.StatusNotFound, "project not found")
		}
		return app.editProject(req, base, id)
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "delete" && req.Method == http.MethodPost:
		return app.deleteProject(base, parts[1])
	default:
		return extension.Error(http.StatusNotFound, "unknown admin route")
	}
}

func (app *projectApp) listProjects(req extension.WireRequest, base string) extension.WireResponse {
	u, err := parseWireURL(req.URL)
	if err != nil {
		return extension.Error(http.StatusBadRequest, err.Error())
	}
	query := strings.TrimSpace(u.Query().Get("q"))
	status := strings.TrimSpace(u.Query().Get("status"))
	if status != "" && !validStatus(status) {
		status = ""
	}
	page := parsePage(u)
	db, err := app.siteDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	filtered := app.searchProjects(db, query, status)
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	var rows []project
	err = app.searchProjects(db, query, status).
		Order("updated_at desc, project_id desc").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&rows).Error
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	counts, err := app.adoptionCounts(db, projectIDs(rows))
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	views := make([]projectView, 0, len(rows))
	for _, row := range rows {
		views = append(views, projectView{
			project:         row,
			CompleteByLabel: formatCompleteBy(row.CompleteBy),
			StatusLabel:     statusLabel(row.Status),
			AdoptionCount:   counts[row.ID],
		})
	}
	return app.render("project/admin_list.html", map[string]any{
		"Base":     base,
		"Rows":     views,
		"Query":    query,
		"Status":   status,
		"Statuses": statusOptions(""),
		"Page":     page,
		"Total":    int(total),
		"HasPrev":  page > 1,
		"HasNext":  page*pageSize < int(total),
	})
}

func (app *projectApp) searchProjects(db *gorm.DB, query, status string) *gorm.DB {
	tx := db.Table(app.projectsTable())
	if status != "" {
		tx = tx.Where("status = ?", status)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return tx
	}
	like := likePattern(query)
	adoptionSQL := "project_id IN (SELECT project_id FROM " + app.adoptionsTable() + " WHERE name LIKE ? OR email LIKE ? OR phone LIKE ? OR note LIKE ? OR proposed_date LIKE ?)"
	return tx.Where(
		"name LIKE ? OR skills LIKE ? OR note LIKE ? OR complete_by LIKE ? OR "+adoptionSQL,
		like, like, like, like, like, like, like, like, like,
	)
}

func (app *projectApp) viewProject(req extension.WireRequest, base string, id uint) extension.WireResponse {
	db, err := app.siteDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	var row project
	err = db.Table(app.projectsTable()).Where("project_id = ?", id).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return extension.Error(http.StatusNotFound, "project not found")
	}
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	people, err := app.projectAdoptions(db, id)
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	u, _ := parseWireURL(req.URL)
	return app.render("project/admin_view.html", map[string]any{
		"Base":            base,
		"Project":         row,
		"StatusLabel":     statusLabel(row.Status),
		"CompleteByLabel": formatCompleteBy(row.CompleteBy),
		"Adoptions":       people,
		"Saved":           u != nil && u.Query().Get("saved") == "1",
		"CSRFField":       csrfField(req),
	})
}

func (app *projectApp) editProject(req extension.WireRequest, base string, id uint) extension.WireResponse {
	row := project{Active: true, Status: statusOpen}
	if id != 0 {
		db, err := app.siteDB()
		if err != nil {
			return extension.Error(http.StatusInternalServerError, err.Error())
		}
		err = db.Table(app.projectsTable()).Where("project_id = ?", id).First(&row).Error
		if err == gorm.ErrRecordNotFound {
			return extension.Error(http.StatusNotFound, "project not found")
		}
		if err != nil {
			return extension.Error(http.StatusInternalServerError, err.Error())
		}
	}
	return app.render("project/admin_form.html", map[string]any{
		"Base":      base,
		"Project":   row,
		"Statuses":  statusOptions(row.Status),
		"IsNew":     id == 0,
		"CSRFField": csrfField(req),
	})
}

func (app *projectApp) saveProject(req extension.WireRequest, base string, id uint) extension.WireResponse {
	values, err := url.ParseQuery(req.Body)
	if err != nil {
		return extension.Error(http.StatusBadRequest, "invalid form submission")
	}
	completeBy, err := parseCompleteBy(values.Get("complete_by"))
	if err != nil {
		return extension.Error(http.StatusBadRequest, err.Error())
	}
	row := project{
		ID:         id,
		Name:       strings.TrimSpace(values.Get("name")),
		Skills:     strings.TrimSpace(values.Get("skills")),
		Note:       strings.TrimSpace(values.Get("note")),
		CompleteBy: completeBy,
		Active:     values.Get("active") == "1",
		Status:     strings.TrimSpace(values.Get("status")),
	}
	switch {
	case row.Name == "":
		return extension.Error(http.StatusBadRequest, "Name is required.")
	case row.Note == "":
		return extension.Error(http.StatusBadRequest, "Description is required.")
	case row.Skills == "":
		return extension.Error(http.StatusBadRequest, "Resources needed is required.")
	case !validStatus(row.Status):
		return extension.Error(http.StatusBadRequest, "Choose a project status.")
	}
	db, err := app.siteDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	now := time.Now().UTC()
	if id == 0 {
		row.CreatedAt = now
		row.UpdatedAt = now
		if err := db.Table(app.projectsTable()).Create(&row).Error; err != nil {
			return extension.Error(http.StatusInternalServerError, err.Error())
		}
		return extension.Redirect(http.StatusSeeOther, base+"/projects/"+strconv.FormatUint(uint64(row.ID), 10)+"?saved=1")
	}
	var count int64
	if err := db.Table(app.projectsTable()).Where("project_id = ?", id).Count(&count).Error; err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	if count == 0 {
		return extension.Error(http.StatusNotFound, "project not found")
	}
	if err := db.Table(app.projectsTable()).Where("project_id = ?", id).Updates(map[string]any{
		"name":        row.Name,
		"skills":      row.Skills,
		"note":        row.Note,
		"complete_by": row.CompleteBy,
		"active":      row.Active,
		"status":      row.Status,
		"updated_at":  now,
	}).Error; err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	return extension.Redirect(http.StatusSeeOther, base+"/projects/"+strconv.FormatUint(uint64(id), 10)+"?saved=1")
}

func (app *projectApp) deleteProject(base, rawID string) extension.WireResponse {
	id, ok := parseID(rawID)
	if !ok {
		return extension.Error(http.StatusNotFound, "project not found")
	}
	db, err := app.siteDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Table(app.adoptionsTable()).Where("project_id = ?", id).Delete(&adoption{}).Error; err != nil {
			return err
		}
		res := tx.Table(app.projectsTable()).Where("project_id = ?", id).Delete(&project{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errString("project not found")
		}
		return nil
	})
	if err != nil {
		if err.Error() == "project not found" {
			return extension.Error(http.StatusNotFound, err.Error())
		}
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	return extension.Redirect(http.StatusSeeOther, base+"?deleted=1")
}

func (app *projectApp) projectAdoptions(db *gorm.DB, id uint) ([]adoption, error) {
	var rows []adoption
	err := db.Table(app.adoptionsTable()).
		Where("project_id = ?", id).
		Order("adoption_id desc").
		Find(&rows).Error
	return rows, err
}

func (app *projectApp) adoptionCounts(db *gorm.DB, ids []uint) (map[uint]int, error) {
	out := map[uint]int{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ProjectID uint `gorm:"column:project_id"`
		Total     int  `gorm:"column:total"`
	}
	err := db.Table(app.adoptionsTable()).
		Select("project_id, COUNT(*) as total").
		Where("project_id IN ?", ids).
		Group("project_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ProjectID] = row.Total
	}
	return out, nil
}

type statusChoice struct {
	Value    string
	Label    string
	Selected bool
}

func statusOptions(current string) []statusChoice {
	options := []statusChoice{
		{Value: statusOpen, Label: "Open"},
		{Value: statusPending, Label: "Pending"},
		{Value: statusInProgress, Label: "In progress"},
		{Value: statusCompleted, Label: "Completed"},
	}
	for i := range options {
		options[i].Selected = options[i].Value == current
	}
	return options
}

func projectIDs(rows []project) []uint {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func (app *projectApp) editPageText(req extension.WireRequest, base string) extension.WireResponse {
	title, intro, err := app.loadPageCopy()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	u, _ := parseWireURL(req.URL)
	saved := u != nil && u.Query().Get("saved") == "1"
	return app.render("project/admin_page.html", map[string]any{
		"Base":         base,
		"Title":        title,
		"Introduction": intro,
		"Saved":        saved,
		"CSRFField":    csrfField(req),
	})
}

func (app *projectApp) savePageText(req extension.WireRequest, base string) extension.WireResponse {
	values, err := url.ParseQuery(req.Body)
	if err != nil {
		return extension.Error(http.StatusBadRequest, "invalid form submission")
	}
	title := strings.TrimSpace(values.Get("title"))
	if title == "" {
		return extension.Error(http.StatusBadRequest, "Heading is required.")
	}
	db, err := app.siteDB()
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	err = extension.DBConfigurationStore(db, app.settingsTable()).Save(pageSection, map[string]any{
		"title":        title,
		"introduction": strings.TrimSpace(values.Get("introduction")),
	})
	if err != nil {
		return extension.Error(http.StatusInternalServerError, err.Error())
	}
	return extension.Redirect(http.StatusSeeOther, base+"/settings?saved=1")
}
