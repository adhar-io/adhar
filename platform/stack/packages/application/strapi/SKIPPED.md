# strapi -- SKIPPED

**Status:** SKIPPED (no official Helm chart, no clean upstream static manifest)

Strapi does not publish or maintain an official Helm chart. The only options are
unofficial community charts (HelmForge, TrueCharts, and various individual GitHub
repos such as `ryuheiyokokawa/strapi-helm` and `rfrancotechnologies/helm-charts`),
none of which are maintained by the upstream Strapi project. Strapi is also a
framework whose deployable artifact is a user-built application image (you scaffold
and build your own app), so there is no canonical, stable image/chart to template.

Per the platform rules we do not fabricate a chart or invent images, so this
package is intentionally left as a stub. To enable it later, adopt a maintained
community chart (e.g. HelmForge) or build a project-specific image + manifests.
