# cannon-ext-project-list

Cannon extension for camp projects that visitors can adopt.

Staff create each project in the admin with a name, a description, the resources needed, and an optional complete-by date. The public page shows the description first. The heading and introduction above the list are edited from Page Text. A project is public only when it is marked active and its status is open. Adopting a project stores the visitor's contact details, leaves the project open for more adoptions, and emits `onProjectListAdopt`. The admin project page opens in a view of every adoption; Edit Project is the form.

## Install

Build this module beside the Cannon checkout. `go.mod` replaces `github.com/rob121/cannon` with `../cannon`.

```bash
go test ./...
go build -o cannon-ext-project-list .
```

Register the binary with Cannon, then activate it for the site. Create a route such as `/projects` and bind the `project-list` page.

## Status

| Status | Shown on the site |
| --- | --- |
| Open, and active | Yes |
| Open, and inactive | No |
| Pending | No |
| In progress | No |
| Completed | No |

The complete-by date is omitted from the public page when it is blank.

## Notification event

| Event | Placeholders |
| --- | --- |
| `onProjectListAdopt` | `project_name`, `skills`, `project_note`, `complete_by`, `name`, `email`, `phone`, `note`, `proposed_date`, `admin_url`, `site_id` |

Subscribe from System → Notifications after the extension is activated.
