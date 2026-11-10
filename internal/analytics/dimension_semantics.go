package analytics

// These tokens are computed for statistics only, never stored as fake domains,
// MIME types or raw boolean values. Filters use the exact same expressions.
const applicationSQL = `(u.device_type='application' OR u.browser='Axolotl' OR
 (e.route='/latest' AND e.platform IS NOT NULL AND e.client_version IS NOT NULL AND
 (u.browser IS NULL OR u.browser='Unknown' OR LOWER(u.user_agent) LIKE '%tauri%')))`

const browserSQL = "CASE WHEN " + applicationSQL + " THEN 'Axolotl' ELSE NULLIF(u.browser,'Unknown') END"
const browserVersionSQL = "CASE WHEN " + applicationSQL + " THEN '@application' ELSE NULLIF(u.browser_version,'') END"
const deviceSQL = "CASE WHEN " + applicationSQL + " THEN 'application' ELSE NULLIF(u.device_type,'unknown') END"
const osSQL = "CASE WHEN " + applicationSQL + ` THEN
 COALESCE(NULLIF(NULLIF(u.os,'Unknown'),''),CASE WHEN e.platform LIKE 'windows-%' THEN 'Windows'
 WHEN e.platform LIKE 'darwin-%' THEN 'macOS'
 WHEN e.platform LIKE 'linux-%' THEN 'Linux'
 ELSE '@not_provided' END)
 ELSE NULLIF(NULLIF(u.os,'Unknown'),'') END`
const refererSQL = "CASE WHEN NULLIF(e.referer_host,'') IS NOT NULL THEN e.referer_host WHEN " + applicationSQL + " THEN '@application' WHEN e.status_code IS NOT NULL THEN '@direct' END"
const contentTypeSQL = `CASE WHEN e.status_code IN (204,304) THEN '@empty'
 WHEN NULLIF(e.content_type,'') IS NOT NULL THEN e.content_type
 WHEN e.response_bytes=0 AND e.method<>'HEAD' THEN '@empty' END`
const errorCodeSQL = `CASE WHEN NULLIF(e.error_code,'') IS NOT NULL THEN e.error_code
 WHEN e.status_code<400 THEN '@none' WHEN e.status_code>=400 THEN '@http_error' END`

func includeSQL(column string) string {
	return "CASE WHEN e.route NOT IN ('/api/downloads/latest','/api/downloads/:version') THEN '@not_applicable' WHEN " + column + " THEN 'true' WHEN " + column + " IS NOT NULL THEN 'false' END"
}

func init() {
	Dimensions["browser"], Dimensions["browser_version"] = browserSQL, browserVersionSQL
	Dimensions["device_type"], Dimensions["os"] = deviceSQL, osSQL
	Dimensions["referer_host"], Dimensions["content_type"], Dimensions["error_code"] = refererSQL, contentTypeSQL, errorCodeSQL
	Dimensions["include_updater"], Dimensions["include_revoked"] = includeSQL("e.include_updater"), includeSQL("e.include_revoked")
}
