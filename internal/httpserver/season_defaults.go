package httpserver

// A configured future season must not capture reports outside a match week.
// Use the report's date, including for historical reports. Before the first
// configured season there is deliberately no inferred season.
const reportFallbackSeasonQuery = `SELECT id FROM seasons
WHERE start_date <= $1::date
ORDER BY start_date DESC, id DESC LIMIT 1`

const captainFormSeasonsQuery = `SELECT id, name FROM seasons
ORDER BY (start_date <= $1::date) DESC, start_date DESC, id DESC`

const defaultSanctionsSeasonQuery = `SELECT id FROM seasons
WHERE NOT is_archived AND start_date <= $1::date
ORDER BY start_date DESC, id DESC LIMIT 1`
