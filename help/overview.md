# Project List

The Project List extension publishes camp projects and records when someone adopts one.

## What it provides

- An admin screen for creating projects, with a view of every adoption and a separate edit form.
- A public `project-list` page that shows only projects marked active and open.
- An adopt form that collects contact details. The project stays open so more people can adopt it.
- The `onProjectListAdopt` notification event.

## Project fields

Each project has a name, a description, a resources-needed section, and an optional complete-by date. The date is shown on the site only when it is set. The public page shows the description before the resources.

Active controls whether the project is allowed on the site. Status is one of open, pending, in progress, or completed. A project appears publicly only when it is both active and open.

## Adopting a project

Bind a site route, for example `/projects`, to the `project-list` page. The heading and introduction are edited from Projects → Page Text. The visitor chooses Adopt This Project, enters a name, email, and phone, and can add an optional note and a proposed date. The form posts to `POST /data/project/adopt`.

A successful adoption records the contact details and leaves the status unchanged. Staff change status from Edit Project. Setting a project to pending, in progress, or completed, or clearing Active, removes it from the public page. Existing adoptions stay on the project view.

## Notifications

Subscribe to `onProjectListAdopt` under System → Notifications after the extension is activated. Placeholders are `project_name`, `skills`, `project_note`, `complete_by`, `name`, `email`, `phone`, `note`, `proposed_date`, `admin_url`, and `site_id`.
