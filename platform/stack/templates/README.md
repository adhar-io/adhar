# Adhar application templates

Source of truth for the service/application templates that `adhar apps deploy
<name> --template <t>` (CLI) and the Adhar Console both instantiate through the
**CompositeApplication** control-plane layer.

Each template is a `CompositeApplication` (platform.adhar.io/v1alpha1) with
`${APP_NAME}` / `${APP_NAMESPACE}` placeholders substituted at creation time.
This directory is pushed to the Gitea `templates` repo during `adhar up`, so the
templates are served from Gitea (the CLI fetches them from there); edit here and
re-run to update.

Available: basic-git, microservice, frontend.
